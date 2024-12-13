package client

import (
	"context"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/cnsync/gateway/proxy/debug"
	"github.com/cnsync/kratos/log"
	"github.com/cnsync/kratos/registry"
	"github.com/google/uuid"
)

// 定义一个错误，表示监控被取消
var ErrCancelWatch = errors.New("cancel watch")

// 创建一个全局的服务监控器实例
var globalServiceWatcher = newServiceWatcher()

// 创建一个日志助手，用于记录日志
var LOG = log.NewHelper(log.With(log.GetLogger(), "source", "servicewatch"))

// 在程序初始化时，注册服务监控器到调试模块
func init() {
	debug.Register("watcher", globalServiceWatcher)
}

// 生成一个 UUID v4 字符串
func uuid4() string {
	return uuid.NewString()
}

// 计算一组服务实例的哈希值，用于比较实例集合是否发生变化
func instancesSetHash(instances []*registry.ServiceInstance) string {
	// 对实例列表进行排序，确保顺序一致
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].ID < instances[j].ID
	})
	// 将实例列表转换为 JSON 格式
	jsBytes, err := json.Marshal(instances)
	if err != nil {
		// 如果转换失败，返回空字符串
		return ""
	}
	// 计算 JSON 数据的 CRC32 哈希值，并将其转换为字符串
	return strconv.FormatUint(uint64(crc32.ChecksumIEEE(jsBytes)), 10)
}

// watcherStatus 结构体定义了监控器的状态，包括监控器实例、初始化通道和选中的实例列表
type watcherStatus struct {
	// 监控器实例
	watcher registry.Watcher
	// 初始化通道，用于通知监控器已初始化完成
	initializedChan chan struct{}
	// 选中的实例列表
	selectedInstances []*registry.ServiceInstance
}

// serviceWatcher 结构体定义了服务监控器，包含读写锁、监控器状态映射和应用程序映射
type serviceWatcher struct {
	// 读写锁，用于保护监控器状态和应用程序映射
	lock sync.RWMutex
	// 监控器状态映射，键为端点名称，值为 watcherStatus 结构体实例
	watcherStatus map[string]*watcherStatus
	// 应用程序映射，键为端点名称，值为应用程序实例映射
	appliers map[string]map[string]Applier
}

// newServiceWatcher 函数创建一个新的服务监控器实例，并启动一个后台清理任务
func newServiceWatcher() *serviceWatcher {
	// 创建一个服务监控器实例
	s := &serviceWatcher{
		// 初始化监控器状态映射
		watcherStatus: make(map[string]*watcherStatus),
		// 初始化应用程序映射
		appliers: make(map[string]map[string]Applier),
	}
	// 启动一个后台清理任务，定期清理过期的监控器和应用程序
	go s.proccleanup()
	// 返回新创建的服务监控器实例
	return s
}

// setSelectedCache 方法设置指定端点的选中实例缓存
func (s *serviceWatcher) setSelectedCache(endpoint string, instances []*registry.ServiceInstance) {
	// 加锁，保护监控器状态映射
	s.lock.Lock()
	// 延迟解锁
	defer s.lock.Unlock()

	// 设置指定端点的选中实例列表
	s.watcherStatus[endpoint].selectedInstances = instances
}

// getSelectedCache 方法获取指定端点的选中实例缓存
func (s *serviceWatcher) getSelectedCache(endpoint string) ([]*registry.ServiceInstance, bool) {
	// 加读锁，保护监控器状态映射
	s.lock.RLock()
	// 延迟解锁
	defer s.lock.RUnlock()

	// 尝试获取指定端点的监控器状态
	ws, ok := s.watcherStatus[endpoint]
	if ok {
		// 如果找到，返回选中的实例列表和 true
		return ws.selectedInstances, true
	}
	// 如果未找到，返回 nil 和 false
	return nil, false
}

// getAppliers 方法获取指定端点的应用程序实例列表
func (s *serviceWatcher) getAppliers(endpoint string) (map[string]Applier, bool) {
	// 加读锁，保护应用程序映射
	s.lock.RLock()
	// 延迟解锁
	defer s.lock.RUnlock()

	// 尝试获取指定端点的应用程序实例映射
	appliers, ok := s.appliers[endpoint]
	if ok {
		// 如果找到，返回应用程序实例映射和 true
		return appliers, true
	}
	// 如果未找到，返回 nil 和 false
	return nil, false
}

// Applier 接口定义了一个应用程序实例，它可以接收服务实例的回调通知，并检查是否已被取消
type Applier interface {
	// Callback 方法接收一组服务实例，并返回一个错误
	Callback([]*registry.ServiceInstance) error
	// Canceled 方法检查应用程序实例是否已被取消
	Canceled() bool
}

// Add 方法用于添加一个新的服务监控器到指定的端点，并注册一个应用程序实例来接收服务实例的回调通知
func (s *serviceWatcher) Add(ctx context.Context, discovery registry.Discovery, endpoint string, applier Applier) (watcherExisted bool) {
	// 加锁，保护监控器状态和应用程序映射
	s.lock.Lock()
	defer s.lock.Unlock()

	// 检查监控器是否已经存在
	existed := func() bool {
		// 尝试获取指定端点的监控器状态
		ws, ok := s.watcherStatus[endpoint]
		if ok {
			// 从初始化通道中接收通知，表明监控器已初始化完成
			<-ws.initializedChan

			// 如果存在选中的实例缓存，则使用这些实例进行回调
			if len(ws.selectedInstances) > 0 {
				LOG.Infof("Using cached %d selected instances on endpoint: %s, hash: %s", len(ws.selectedInstances), endpoint, instancesSetHash(ws.selectedInstances))
				// 调用应用程序实例的回调方法，传递选中的实例列表
				applier.Callback(ws.selectedInstances)
				return true
			}

			return true
		}

		// 如果监控器不存在，则创建一个新的监控器状态实例
		ws = &watcherStatus{
			initializedChan: make(chan struct{}),
		}
		// 使用发现服务创建一个新的监控器实例
		watcher, err := discovery.Watch(ctx, endpoint)
		if err != nil {
			// 如果创建失败，记录错误并返回 false
			LOG.Errorf("Failed to initialize watcher on endpoint: %s, err: %+v", endpoint, err)
			return false
		}
		// 记录成功初始化监控器的信息
		LOG.Infof("Succeeded to initialize watcher on endpoint: %s", endpoint)
		// 将新创建的监控器实例保存到监控器状态中
		ws.watcher = watcher
		// 将监控器状态保存到服务监控器的状态映射中
		s.watcherStatus[endpoint] = ws

		// 启动一个 goroutine 来执行初始化服务发现
		func() {
			defer close(ws.initializedChan)
			LOG.Infof("Starting to do initialize services discovery on endpoint: %s", endpoint)
			// 获取初始的服务实例列表
			services, err := watcher.Next()
			if err != nil {
				// 如果获取失败，记录错误并返回
				LOG.Errorf("Failed to do initialize services discovery on endpoint: %s, err: %+v, the watch process will attempt asynchronously", endpoint, err)
				return
			}
			// 记录成功获取初始服务实例列表的信息
			LOG.Infof("Succeeded to do initialize services discovery on endpoint: %s, %d services, hash: %s", endpoint, len(services), instancesSetHash(ws.selectedInstances))
			// 将获取到的服务实例列表保存到监控器状态中
			ws.selectedInstances = services
			// 调用应用程序实例的回调方法，传递初始服务实例列表
			applier.Callback(services)
		}()

		// 启动一个 goroutine 来持续监控服务实例的变化
		go func() {
			for {
				// 获取最新的服务实例列表
				services, err := watcher.Next()
				if err != nil {
					// 如果获取失败，检查错误类型
					if errors.Is(err, context.Canceled) {
						// 如果是上下文取消，则记录警告并返回
						LOG.Warnf("The watch process on: %s has been canceled", endpoint)
						return
					}
					// 如果是其他错误，则记录错误并等待 1 秒后重试
					LOG.Errorf("Failed to watch on endpoint: %s, err: %+v, the watch process will attempt again after 1 second", endpoint, err)
					time.Sleep(time.Second)
					continue
				}
				// 如果获取到的服务实例列表为空，则记录警告并继续
				if len(services) == 0 {
					LOG.Warnf("Empty services on endpoint: %s, this most likely no available instance in discovery", endpoint)
					continue
				}
				// 记录接收到的服务实例列表信息
				LOG.Infof("Received %d services on endpoint: %s, hash: %s", len(services), endpoint, instancesSetHash(services))
				// 将获取到的服务实例列表保存到缓存中
				s.setSelectedCache(endpoint, services)
				// 调用回调方法，通知应用程序实例服务实例列表的变化
				s.doCallback(endpoint, services)
			}
		}()

		return false
	}()

	// 记录添加应用程序实例的信息
	LOG.Infof("Add appliers on endpoint: %s", endpoint)
	// 如果应用程序实例不为空，则将其注册到服务监控器的应用程序映射中
	if applier != nil {
		if _, ok := s.appliers[endpoint]; !ok {
			// 如果端点的应用程序实例映射不存在，则创建一个新的映射
			s.appliers[endpoint] = make(map[string]Applier)
		}
		// 为应用程序实例生成一个唯一的标识符，并将其保存到映射中
		s.appliers[endpoint][uuid4()] = applier
	}

	// 返回监控器是否已经存在的标志
	return existed
}

// doCallback 方法用于遍历指定端点的所有应用程序实例，并调用它们的回调方法来处理服务实例的变化
func (s *serviceWatcher) doCallback(endpoint string, services []*registry.ServiceInstance) {
	// 记录被取消的应用程序实例数量
	canceled := 0
	// 启动一个匿名函数，在函数内部加读锁，保护应用程序映射
	func() {
		s.lock.RLock()
		defer s.lock.RUnlock()
		// 遍历指定端点的所有应用程序实例
		for id, applier := range s.appliers[endpoint] {
			// 调用应用程序实例的回调方法，传递服务实例列表
			if err := applier.Callback(services); err != nil {
				// 如果回调方法返回错误，检查错误类型
				if errors.Is(err, ErrCancelWatch) {
					// 如果是监控被取消错误，记录警告并增加已取消计数
					canceled += 1
					LOG.Warnf("appliers on endpoint: %s, id: %s is canceled, will delete later", endpoint, id)
					continue
				}
				// 如果是其他错误，记录错误信息
				LOG.Errorf("Failed to call appliers on endpoint: %q: %+v", endpoint, err)
			}
		}
	}()
	// 如果没有被取消的应用程序实例，直接返回
	if canceled <= 0 {
		return
	}
	// 记录有被取消的应用程序实例的信息
	LOG.Warnf("There are %d canceled appliers on endpoint: %q, will be deleted later in cleanup proc", canceled, endpoint)
}

// proccleanup 方法启动一个后台任务，定期清理已取消的应用程序实例
func (s *serviceWatcher) proccleanup() {
	// 定义一个内部函数 doCleanup，用于执行清理操作
	doCleanup := func() {
		// 遍历所有端点的应用程序实例映射
		for endpoint, appliers := range s.appliers {
			// 初始化一个切片，用于存储需要清理的应用程序实例的 ID
			var cleanup []string
			// 启动一个匿名函数，在函数内部加读锁，保护应用程序映射
			func() {
				s.lock.RLock()
				defer s.lock.RUnlock()
				// 遍历当前端点的所有应用程序实例
				for id, applier := range appliers {
					// 如果应用程序实例已被取消，则将其 ID 添加到清理列表中
					if applier.Canceled() {
						cleanup = append(cleanup, id)
						// 记录警告信息，表示该应用程序实例将被删除
						LOG.Warnf("applier on endpoint: %s, id: %s is canceled, will be deleted later", endpoint, id)
						continue
					}
				}
			}()
			// 如果没有需要清理的应用程序实例，则直接返回
			if len(cleanup) <= 0 {
				return
			}
			// 记录清理信息，包括端点名称和需要清理的应用程序实例 ID
			LOG.Infof("Cleanup appliers on endpoint: %q with keys: %+v", endpoint, cleanup)
			// 启动一个匿名函数，在函数内部加锁，保护应用程序映射
			func() {
				s.lock.Lock()
				defer s.lock.Unlock()
				// 遍历清理列表，删除对应的应用程序实例
				for _, id := range cleanup {
					delete(appliers, id)
				}
				// 记录清理结果，包括清理的应用程序实例数量和当前端点剩余的应用程序实例数量
				LOG.Infof("Succeeded to clean %d appliers on endpoint: %q, now %d appliers are available", len(cleanup), endpoint, len(appliers))
			}()
		}
	}

	// 定义清理间隔时间为 30 秒
	const interval = time.Second * 30
	// 启动一个无限循环，定期执行清理任务
	for {
		// 记录开始清理的信息
		LOG.Infof("Start to cleanup appliers on all endpoints for every %s", interval.String())
		// 等待清理间隔时间
		time.Sleep(interval)
		// 执行清理操作
		doCleanup()
	}

}

// DebugHandler 函数返回一个 HTTP 处理器，用于处理调试请求
func (s *serviceWatcher) DebugHandler() http.Handler {
	// 创建一个新的 HTTP 多路复用器
	debugMux := http.NewServeMux()
	// 注册一个处理函数，用于处理 /debug/watcher/nodes 路径的请求
	debugMux.HandleFunc("/debug/watcher/nodes", func(w http.ResponseWriter, r *http.Request) {
		// 从请求的 URL 查询参数中获取服务名称
		service := r.URL.Query().Get("service")
		// 从服务监控器中获取选中的节点缓存
		nodes, _ := s.getSelectedCache(service)
		// 设置响应头的 Content-Type 为 application/json
		w.Header().Set("Content-Type", "application/json")
		// 使用 JSON 编码器将节点列表编码并写入响应
		json.NewEncoder(w).Encode(nodes)
	})
	// 注册一个处理函数，用于处理 /debug/watcher/appliers 路径的请求
	debugMux.HandleFunc("/debug/watcher/appliers", func(w http.ResponseWriter, r *http.Request) {
		// 从请求的 URL 查询参数中获取服务名称
		service := r.URL.Query().Get("service")
		// 从服务监控器中获取应用程序实例列表
		appliers, _ := s.getAppliers(service)
		// 设置响应头的 Content-Type 为 application/json
		w.Header().Set("Content-Type", "application/json")
		// 使用 JSON 编码器将应用程序实例列表编码并写入响应
		json.NewEncoder(w).Encode(appliers)
	})
	// 返回创建的 HTTP 处理器
	return debugMux
}

// AddWatch 函数用于向全局服务监控器添加一个新的监控器和应用程序实例
func AddWatch(ctx context.Context, registry registry.Discovery, endpoint string, applier Applier) bool {
	// 调用全局服务监控器的 Add 方法，添加监控器和应用程序实例
	return globalServiceWatcher.Add(ctx, registry, endpoint, applier)
}
