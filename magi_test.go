package magi

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/evanhuang8/magi/cluster"
	"github.com/evanhuang8/magi/job"
	"github.com/evanhuang8/magi/lock"
)

func FlushQueue() {
	cmd := exec.Command("./test/disque/flush.sh")
	err := cmd.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return
}

func TestMain(m *testing.M) {
	flag.Parse()
	FlushQueue()
	code := m.Run()
	FlushQueue()
	os.Exit(code)
}

var disqueHosts = []map[string]interface{}{
	map[string]interface{}{
		"address": "127.0.0.1:7711",
	},
	map[string]interface{}{
		"address": "127.0.0.1:7712",
	},
	map[string]interface{}{
		"address": "127.0.0.1:7713",
	},
}

var dqConfig = &cluster.DisqueClusterConfig{
	Hosts: disqueHosts,
}

var disqueHostsSingle = []map[string]interface{}{
	map[string]interface{}{
		"address": "127.0.0.1:7711",
	},
}

var dqsConfig = &cluster.DisqueClusterConfig{
	Hosts: disqueHostsSingle,
}

var redisHosts = []map[string]interface{}{
	map[string]interface{}{
		"address": "127.0.0.1:7777",
	},
	map[string]interface{}{
		"address": "127.0.0.1:7778",
	},
	map[string]interface{}{
		"address": "127.0.0.1:7779",
	},
}

var rConfig = &cluster.RedisClusterConfig{
	Hosts: redisHosts,
}

var redisHostsSingle = []map[string]interface{}{
	map[string]interface{}{
		"address": "127.0.0.1:7777",
	},
}

var rsConfig = &cluster.RedisClusterConfig{
	Hosts: redisHostsSingle,
}

func RandomKey() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		panic(errors.New("Fail to generate random bytes!"))
	}
	key := fmt.Sprintf("lockkey%X%X%X%X%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
	return key
}

func TestProducer(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	producer, err := Producer(dqConfig)
	assert.Empty(err)
	assert.NotEmpty(producer)
	defer producer.Close()
	queue := "jobq" + RandomKey()
	// Add job
	delay, _ := time.ParseDuration("10s")
	eta := time.Now().Add(delay)
	job, err := producer.AddJob(queue, "job1", eta, nil)
	assert.Empty(err)
	assert.NotEmpty(job)
	assert.NotEmpty(job.ID)
	assert.Equal(job.Body, "job1")
	// Get job
	_job, err := producer.GetJob(job.ID)
	assert.Empty(err)
	assert.NotEmpty(_job)
	assert.Equal(job.Body, _job.Body)
}

func TestLockAcquisition(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	c := cluster.NewRedisCluster(rConfig)
	assert.NotEmpty(c)
	defer c.Close()
	// Create lock
	key := RandomKey()
	l := lock.CreateLock(c, key)
	l.Duration = 3 * time.Second
	// Acquire lock
	success, err := l.Get(false)
	assert.Empty(err)
	assert.True(success)
	assert.True(l.IsActive())
}

func TestLockMutualExclusion(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	c := cluster.NewRedisCluster(rConfig)
	assert.NotEmpty(c)
	defer c.Close()
	// Create locks
	key := RandomKey()
	l1 := lock.CreateLock(c, key)
	l2 := lock.CreateLock(c, key)
	l1.Duration = 3 * time.Second
	l2.Duration = 3 * time.Second
	// Acquire lock on l1
	success, err := l1.Get(false)
	assert.Empty(err)
	assert.True(success)
	assert.True(l1.IsActive())
	// Acquire lock on l2, should fail
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.False(success)
	assert.False(l2.IsActive())
}

func TestLockIsolation(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	c := cluster.NewRedisCluster(rConfig)
	assert.NotEmpty(c)
	defer c.Close()
	// Create locks
	key1 := RandomKey()
	key2 := RandomKey()
	l1 := lock.CreateLock(c, key1)
	l2 := lock.CreateLock(c, key2)
	l1.Duration = 16 * time.Second
	l2.Duration = 16 * time.Second
	// Acquire lock on l1
	success, err := l1.Get(false)
	assert.Empty(err)
	assert.True(success)
	assert.True(l1.IsActive())
	// Acquire lock on l2
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.True(success)
	assert.True(l2.IsActive())
}

func TestLockRelease(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	c := cluster.NewRedisCluster(rConfig)
	assert.NotEmpty(c)
	defer c.Close()
	// Create lock
	key := RandomKey()
	l1 := lock.CreateLock(c, key)
	l1.Duration = 16 * time.Second
	l2 := lock.CreateLock(c, key)
	// Acquire lock
	success, err := l1.Get(false)
	assert.Empty(err)
	assert.True(success)
	assert.True(l1.IsActive())
	// Acquire lock on the same key, should fail
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.False(success)
	assert.False(l2.IsActive())
	// Release original lock
	success, err = l1.Release()
	assert.Empty(err)
	assert.True(success)
	assert.False(l1.IsActive())
	// Acquire lock, should succeed this time
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.True(success)
	assert.True(l2.IsActive())
}

func TestLockAutoExpire(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	c := cluster.NewRedisCluster(rConfig)
	assert.NotEmpty(c)
	defer c.Close()
	// Create lock
	key := RandomKey()
	l1 := lock.CreateLock(c, key)
	duration := 3 * time.Second
	l1.Duration = duration
	l2 := lock.CreateLock(c, key)
	// Acquire lock
	success, err := l1.Get(false)
	assert.Empty(err)
	assert.True(success)
	// Acquire lock on the same key, should fail
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.False(success)
	// Sleep past the expiration time
	time.Sleep(duration)
	// Acquire lock, should succeed this time
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.True(success)
}

func TestLockAutoRenew(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	c := cluster.NewRedisCluster(rConfig)
	assert.NotEmpty(c)
	defer c.Close()
	// Create lock
	key := RandomKey()
	l1 := lock.CreateLock(c, key)
	duration := 5 * time.Second
	l1.Duration = duration
	l2 := lock.CreateLock(c, key)
	// Acquire lock
	success, err := l1.Get(true)
	assert.Empty(err)
	assert.True(success)
	// Acquire lock on the same key, should fail
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.False(success)
	// Sleep past the expiration time
	time.Sleep(duration)
	// Acquire lock on the same key, should still fail
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.False(success)
	// Release original lock
	success, err = l1.Release()
	assert.Empty(err)
	assert.True(success)
	assert.False(l1.IsActive())
	// Acquire lock, should succeed this time
	success, err = l2.Get(false)
	assert.Empty(err)
	assert.True(success)
}

func TestLockContestDuo(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	c := cluster.NewRedisCluster(rConfig)
	assert.NotEmpty(c)
	defer c.Close()
	// Create 2 locks on the same key
	key := RandomKey()
	locks := []*lock.Lock{
		lock.CreateLock(c, key),
		lock.CreateLock(c, key),
	}
	// Unlease dogs of war
	result := make(chan int, 2)
	acquired := 0
	for _, l := range locks {
		func(l *lock.Lock) {
			go func() {
				success, err := l.Get(false)
				assert.Empty(err)
				if success {
					acquired++
				}
				result <- 1
			}()
		}(l)
	}
	// Wait for them to finish
	<-result
	<-result
	assert.Equal(acquired, 1)
}

func TestLockContestTrio(t *testing.T) {
	assert := assert.New(t)
	// Instantiation
	clusters := []*cluster.RedisCluster{
		cluster.NewRedisCluster(rConfig),
		cluster.NewRedisCluster(rConfig),
		cluster.NewRedisCluster(rConfig),
	}
	defer func() {
		for _, c := range clusters {
			c.Close()
		}
	}()
	// Create 3 locks on the same key
	key := RandomKey()
	locks := []*lock.Lock{}
	for _, c := range clusters {
		l := lock.CreateLock(c, key)
		locks = append(locks, l)
	}
	// Unlease dogs of war
	result := make(chan int, 3)
	acquired := 0
	for _, l := range locks {
		func(l *lock.Lock) {
			go func() {
				success, err := l.Get(false)
				assert.Empty(err)
				if success {
					acquired++
				}
				result <- 1
			}()
		}(l)
	}
	// Wait for them to finish
	for i := 0; i < 3; i++ {
		<-result
	}
	assert.True(acquired <= 1)
}

type DummyProcessor struct {
	Bodies []string
	mutex  sync.Mutex
}

func (p *DummyProcessor) Process(job *job.Job) (interface{}, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.Bodies = append(p.Bodies, job.Body+"dummy")
	return true, nil
}

func (p *DummyProcessor) ShouldAutoRenew(job *job.Job) bool {
	return true
}

func TestConsumer(t *testing.T) {
	assert := assert.New(t)
	FlushQueue()
	// Instantiation
	consumer, err := Consumer(dqConfig, rConfig)
	assert.Empty(err)
	assert.NotEmpty(consumer)
	defer consumer.Close()
	queue := "jobq" + RandomKey()
	// Add a job
	job, err := consumer.AddJob(queue, "job2", time.Now(), nil)
	assert.Empty(err)
	assert.NotEmpty(job)
	assert.NotEmpty(job.ID)
	assert.Equal(job.Body, "job2")
	// Setup the processor
	p := &DummyProcessor{}
	consumer.Register(queue, p)
	// Kick off processing
	go consumer.Process(queue)
	time.Sleep(2 * time.Second)
	assert.True(consumer.IsProcessing())
	// Wait for it to be processed
	time.Sleep(1 * time.Second)
	assert.Equal(p.Bodies[0], job.Body+"dummy")
}

func TestConsumerThroughPutSingleQueue(t *testing.T) {
	assert := assert.New(t)
	FlushQueue()
	// Instantiation
	consumer, err := Consumer(dqsConfig, rConfig)
	assert.Empty(err)
	assert.NotEmpty(consumer)
	defer consumer.Close()
	queue := "jobq" + RandomKey()
	// Add jobs
	n := 100
	bodies := make([]string, 0, n)
	eta := time.Now()
	conf := &cluster.DisqueOpConfig{
		Replicate: 1,
	}
	for i := 0; i < n; i++ {
		body := RandomKey()
		job, err := consumer.AddJob(queue, body, eta, conf)
		assert.Empty(err)
		assert.NotEmpty(job)
		assert.NotEmpty(job.ID)
		assert.Equal(job.Body, body)
		bodies = append(bodies, body)
	}
	// Setup the processor
	p := &DummyProcessor{
		Bodies: make([]string, 0, n),
	}
	consumer.Register(queue, p)
	// Kick off processing
	go consumer.Process(queue)
	time.Sleep(2 * time.Second)
	assert.True(consumer.IsProcessing())
	// Wait for it to be processed
	time.Sleep(2 * time.Second)
	assert.Equal(len(p.Bodies), n)
	for _, body := range bodies {
		isProcessed := false
		for _, _body := range p.Bodies {
			if body+"dummy" == _body {
				isProcessed = true
			}
		}
		assert.True(isProcessed)
	}
}

func TestConsumerThroughPut(t *testing.T) {
	assert := assert.New(t)
	FlushQueue()
	// Instantiation
	consumer, err := Consumer(dqConfig, rConfig)
	assert.Empty(err)
	assert.NotEmpty(consumer)
	defer consumer.Close()
	queue := "jobq" + RandomKey()
	// Add jobs
	n := 100
	bodies := make([]string, 0, n)
	eta := time.Now()
	for i := 0; i < n; i++ {
		body := RandomKey()
		job, err := consumer.AddJob(queue, body, eta, nil)
		assert.Empty(err)
		assert.NotEmpty(job)
		assert.NotEmpty(job.ID)
		assert.Equal(job.Body, body)
		bodies = append(bodies, body)
	}
	// Setup the processor
	p := &DummyProcessor{
		Bodies: make([]string, 0, n),
	}
	consumer.Register(queue, p)
	// Kick off processing
	go consumer.Process(queue)
	time.Sleep(2 * time.Second)
	assert.True(consumer.IsProcessing())
	// Wait for it to be processed
	time.Sleep(3 * time.Second)
	assert.Equal(len(p.Bodies), n)
	for _, body := range bodies {
		isProcessed := false
		for _, _body := range p.Bodies {
			if body+"dummy" == _body {
				isProcessed = true
			}
		}
		assert.True(isProcessed)
	}
}

func TestConsumerDelay(t *testing.T) {
	assert := assert.New(t)
	FlushQueue()
	// Instantiation
	consumer, err := Consumer(dqConfig, rConfig)
	assert.Empty(err)
	assert.NotEmpty(consumer)
	defer consumer.Close()
	queue := "jobq" + RandomKey()
	// Add delay job
	eta := time.Now().Add(5 * time.Second)
	body := RandomKey()
	job, err := consumer.AddJob(queue, body, eta, nil)
	assert.Empty(err)
	assert.NotEmpty(job)
	assert.NotEmpty(job.ID)
	assert.Equal(job.Body, body)
	// Setup the processor
	p := &DummyProcessor{
		Bodies: make([]string, 0, 1),
	}
	consumer.Register(queue, p)
	// Kick off processing
	go consumer.Process(queue)
	time.Sleep(2 * time.Second)
	assert.True(consumer.IsProcessing())
	// Check delay behavior
	time.Sleep(time.Second)
	assert.Equal(len(p.Bodies), 0)
	time.Sleep(2 * time.Second)
	assert.Equal(len(p.Bodies), 1)
	assert.Equal(p.Bodies[0], body+"dummy")
}

func TestConsumerDelayDelete(t *testing.T) {
	assert := assert.New(t)
	FlushQueue()
	// Instantiation
	consumer, err := Consumer(dqConfig, rConfig)
	assert.Empty(err)
	assert.NotEmpty(consumer)
	defer consumer.Close()
	queue := "jobq" + RandomKey()
	// Add delay job
	eta := time.Now().Add(5 * time.Second)
	body := RandomKey()
	job, err := consumer.AddJob(queue, body, eta, nil)
	assert.Empty(err)
	assert.NotEmpty(job)
	assert.NotEmpty(job.ID)
	assert.Equal(job.Body, body)
	// Setup the processor
	p := &DummyProcessor{
		Bodies: make([]string, 0, 1),
	}
	consumer.Register(queue, p)
	// Kick off processing
	go consumer.Process(queue)
	time.Sleep(2 * time.Second)
	assert.True(consumer.IsProcessing())
	// Check delay behavior
	time.Sleep(time.Second)
	assert.Equal(len(p.Bodies), 0)
	// Delete job
	result, err := consumer.DeleteJob(job.ID)
	assert.Empty(err)
	assert.True(result)
	// Job should not be processed
	time.Sleep(2 * time.Second)
	assert.Equal(len(p.Bodies), 0)
	// Get job
	_job, err := consumer.GetJob(job.ID)
	assert.Empty(err)
	assert.Empty(_job)
}

func TestConsumerDelayOrder(t *testing.T) {
	assert := assert.New(t)
	FlushQueue()
	// Instantiation
	consumer, err := Consumer(dqsConfig, rConfig)
	assert.Empty(err)
	assert.NotEmpty(consumer)
	defer consumer.Close()
	queue := "jobq" + RandomKey()
	// Add jobs
	n := 20
	bodies := make([]string, 0, n)
	for i := 0; i < n; i++ {
		body := RandomKey()
		eta := time.Now().Add(time.Duration(i*100) * time.Millisecond)
		job, err := consumer.AddJob(queue, body, eta, nil)
		assert.Empty(err)
		assert.NotEmpty(job)
		assert.NotEmpty(job.ID)
		assert.Equal(job.Body, body)
		bodies = append(bodies, body)
	}
	// Setup the processor
	p := &DummyProcessor{
		Bodies: make([]string, 0, n),
	}
	consumer.Register(queue, p)
	// Kick off processing
	go consumer.Process(queue)
	time.Sleep(2 * time.Second)
	assert.True(consumer.IsProcessing())
	// Wait for it to be processed
	time.Sleep(5 * time.Second)
	assert.Equal(len(p.Bodies), n)
	for i, body := range bodies {
		assert.Equal(p.Bodies[i], body+"dummy")
	}
}
