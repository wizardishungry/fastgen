package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ENV_VAR     = "FASTGEN_SOCK"
	TMP_PATTERN = "fastgen"
)

func main() {
	if err := main2(); err != nil {
		log.Fatalf("main: %v", err)
	}
}

func main2() error {
	sock, isClient := os.LookupEnv(ENV_VAR)

	defaultParallel := runtime.NumCPU() * 4
	if defaultParallel < 8 {
		defaultParallel = 8
	}

	parallel := flag.Int("parallel", defaultParallel, "number of runners (-1 for unlimited)")
	_ = parallel

	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	if isClient {
		return client(sock, args)
	}

	return server(args)
}

func client(sock string, args []string) error {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("net.Dial: %w", err)
	}
	client := rpc.NewClient(conn)

	if len(args) < 1 {
		return fmt.Errorf("need more args: label [cmd] [args...]")
	}

	var (
		req    any
		resp   any
		method = ""
	)
	if len(args) == 1 { // start or stop cmd
		start, wait, err := parseLabel(args[0])

		if err != nil {
			return fmt.Errorf("parseLabel: %w", err)
		}
		if start != nil {
			req = start
			resp = &CreateQueueResponse{}
			method = "Fastgen.CreateQueue"
		}
		if wait != nil {
			req = wait
			resp = &DoneQueueResponse{}
			method = "Fastgen.DoneQueue"
		}
	} else {
		req = &CreateTaskRequest{
			Task: Task{
				Cmd: args[1:],
				// TODO copy env
			},
			CreateQueueRequest: CreateQueueRequest{
				Queue: args[0],
				Width: 0,
			},
		}
		resp = &CreateTaskResponse{}
		method = "Fastgen.CreateTask"
	}

	return client.Call(method, req, resp)
}

func parseLabel(arg string) (*CreateQueueRequest, *DoneQueueRequest, error) {
	parts := strings.Split(arg, "/")

	var (
		queueName string
		isWait    bool
		width     int
		deps      []string
	)

	if len(parts) == 0 {
		return nil, nil, errors.New("problem splitting label")
	}

	if parts[0] == "" { // "/protoc/0/downloadprotos,installprotoc"
		parts = parts[1:]
		isWait = true
	}
	queueName = parts[0]

	if queueName == "" {
		return nil, nil, errors.New("empty label")
	}

	if len(parts) > 1 && parts[1] != "" {
		i, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("problem parsing width: %w", err)
		}
		width = int(i)
	}

	if len(parts) > 2 {
		deps = strings.Split(parts[2], ",")
		fmt.Println("deps are ", deps)
	}

	if isWait {
		return nil, &DoneQueueRequest{
			Queue: queueName,
		}, nil
	}

	return &CreateQueueRequest{
		Queue: queueName,
		Width: width,
		Deps:  deps,
	}, nil, nil
}

func server(args []string) error {
	tmp, err := os.MkdirTemp("", TMP_PATTERN)
	if err != nil {
		return fmt.Errorf("os.MkdirTemp: %w", err)
	}
	defer os.RemoveAll(tmp)

	sock := filepath.Join(tmp, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("net.Listen: %w", err)
	}
	defer l.Close()

	if err := os.Setenv(ENV_VAR, sock); err != nil {
		log.Fatalf("os.Setenv: %v", err)
	}
	unixL := l.(*net.UnixListener)

	fg := serve(unixL)

	fmt.Println("server", args)

	ctx := context.Background()
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Stderr = os.Stderr
	c.Stdout = os.Stdout
	defer func() {
		fmt.Println("WaitAllQueues")

		err := fg.WaitAllQueues(&WaitAllQueuesRequest{}, &WaitAllQueuesResponse{})
		fmt.Println("🍕", c.ProcessState.ExitCode(), err)
	}()
	return c.Run()
}

func serve(l *net.UnixListener) *Fastgen {

	fg := &Fastgen{
		queues: make(map[string]*queueEntry),
		tasks:  make(chan func()),
	}
	rpc.Register(fg)

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go rpc.ServeConn(conn)
		}
	}()

	go fg.consumeTasks()

	return fg
}

func (fg *Fastgen) consumeTasks() {
	var wg sync.WaitGroup
	defer wg.Wait()
	numWorkers := runtime.NumCPU() // TODO
	wg.Add(numWorkers)
	// TODO restructure to use semaphores and new goroutines
	// detect deadlocks?
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			for task := range fg.tasks {
				task()
			}
		}()
	}
}

type AwaitQueue struct {
	Queue string
}

type Fastgen struct {
	mutex  sync.Mutex
	queues map[string]*queueEntry
	tasks  chan func()
}

type queueEntry struct {
	wg        sync.WaitGroup
	done      chan struct{}
	tasks     []Task
	deps      []string
	semaphore chan struct{}
	wake      chan string
}

type Task struct {
	Cmd []string
	// TODO copy env
	done bool
}

type CreateQueueRequest struct {
	Queue string
	Width int
	Deps  []string
}

type CreateQueueResponse struct{}

func (fg *Fastgen) CreateQueue(req *CreateQueueRequest, resp *CreateQueueResponse) error {
	fmt.Println("CreateQueue", req)
	fg.mutex.Lock()
	defer fg.mutex.Unlock()

	_, _ = fg.findCreateQueue(req.Queue, req.Width, req.Deps)

	return nil
}

func (fg *Fastgen) findCreateQueue(queue string, size int, deps []string) (_ *queueEntry, found bool) {
	q, ok := fg.queues[queue]
	if !ok {
		q = &queueEntry{
			done: make(chan struct{}),
			deps: deps,
			wake: make(chan string, 1),
		}
		if size != 0 {
			q.semaphore = make(chan struct{}, size)
		}
		fg.queues[queue] = q

		go fg.serveQueue(q, queue)

	}

	return q, ok
}

type CreateTaskRequest struct {
	Task
	CreateQueueRequest
}

type CreateTaskResponse struct{}

func (fg *Fastgen) CreateTask(req *CreateTaskRequest, resp *CreateQueueResponse) error {
	fg.mutex.Lock()
	defer fg.mutex.Unlock()

	q, found := fg.findCreateQueue(req.Queue, req.Width, req.Deps)
	q.tasks = append(q.tasks, req.Task)
	fmt.Println("CreateTask", req.Queue, len(q.tasks))

	if found &&
		(len(req.Deps) != 0 || req.Width != 0) {
		return fmt.Errorf("cannot change deps or width once created'")
	}

	return nil
}

func (fg *Fastgen) serveQueue(q *queueEntry, qn string) {
	depMap := make(map[string]struct{}, len(q.deps))
	for _, dep := range q.deps {
		depMap[dep] = struct{}{}
		fmt.Println("depMap", dep)
	}
	var once sync.Once
	for {

		if len(depMap) == 0 {
			once.Do(func() { go fg.executeQueue(q, qn) })
		}

		fmt.Println("waiting", qn, len(depMap), depMap)

		doneQueueName := <-q.wake

		fmt.Println("got queue", doneQueueName)

		delete(depMap, doneQueueName)
	}
}

type runnableTask struct {
	Task
	Result chan error
}

func (fg *Fastgen) executeQueue(q *queueEntry, qn string) {
	defer fg.completeQueue(q, qn)
	fmt.Println("executeQueue")

GET_MORE:
	for {
		fg.mutex.Lock()
		tasks := make([]Task, 0, len(q.tasks))
		tasks = append(tasks, q.tasks...)
		q.tasks = make([]Task, 0, len(q.tasks))
		fg.mutex.Unlock()
		for _, task := range tasks {
			fg.submitTask(q, qn, task)
			if task.done {
				fmt.Println("break")
				break GET_MORE
			}
		}
	}
	fmt.Println("wait")
}

func (fg *Fastgen) submitTask(q *queueEntry, qn string, task Task) {
	q.wg.Add(1)
	if q.semaphore != nil {
		q.semaphore <- struct{}{}
	}
	fg.tasks <- func() {
		defer q.wg.Done()
		if q.semaphore != nil {
			defer func() { <-q.semaphore }()
		}
		if task.done {
			return
		}
		task.execute()
		fmt.Println("exec")
	}
}

func (fg *Fastgen) completeQueue(q *queueEntry, qn string) {
	fg.mutex.Lock()
	defer fg.mutex.Unlock()

	q.wg.Wait()

	fmt.Println("completeQueue")
	close(q.done)
	for _, nextQ := range fg.queues {
		nextQ.wake <- qn
	}
}

func (t *Task) execute() {
	fmt.Println("exec", t.Cmd)
	ctx := context.Background()

	c := exec.CommandContext(ctx, t.Cmd[0], t.Cmd[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	fmt.Println("run", c)
	err := c.Run()
	fmt.Println("done", c)

	if err != nil {
		log.Panicf("c.Run(): %v", err)
	}
}

type DoneQueueRequest struct {
	Queue string
	Wait  bool
}

type DoneQueueResponse struct{}

func (fg *Fastgen) DoneQueue(req *DoneQueueRequest, resp *DoneQueueResponse) error {
	fmt.Println("DoneQueue", req.Queue)
	fg.mutex.Lock()
	q, found := fg.findCreateQueue(req.Queue, 0, nil)
	q.tasks = append(q.tasks, Task{done: true})
	fg.mutex.Unlock()
	if !found {
		return errors.New("can't wait for nonexistant queue")
	}
	if req.Wait {
		<-q.done
	}

	return nil

}

type WaitAllQueuesRequest struct {
	Queue     string
	SkipClose bool
}

type WaitAllQueuesResponse struct{}

func (fg *Fastgen) WaitAllQueues(req *WaitAllQueuesRequest, resp *WaitAllQueuesResponse) error {
	fmt.Println("WAQWAQ")
	fg.mutex.Lock()
	queues := make([]*queueEntry, 0, len(fg.queues))
	for _, q := range fg.queues {
		queues = append(queues, q)
		if !req.SkipClose {
			q.tasks = append(q.tasks, Task{done: true})
		}
	}
	fg.mutex.Unlock()

	for _, q := range queues {
		t := time.Now()
		<-q.done
		fmt.Println("waitq", time.Since(t))
	}

	return nil
}
