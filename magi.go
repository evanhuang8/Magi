package magi

import (
	"errors"
	"fmt"
	"time"

	"github.com/evanhuang8/magi/cluster"
	"github.com/evanhuang8/magi/job"
	"github.com/evanhuang8/magi/lock"
)

// MagiAPIVersion is the current API version
var MagiAPIVersion = "0.1"

// BlockingTimeout is the timeout used for blocking operations
var BlockingTimeout = "5s"

// Magi represents the top level queue application
type Magi struct {
	APIVersion string

	dqCluster *cluster.DisqueCluster
	rCluster  *cluster.RedisCluster

	processors     map[string]*Processor
	isProcessing   bool
	processControl chan string
}

var (
	// MagiProcessCommandStop is the command for stopping the processor
	MagiProcessCommandStop = "STOP"
)

// Producer creates a Magi instance that acts as a producer
func Producer(config *cluster.DisqueClusterConfig) (*Magi, error) {
	dqCluster, err := cluster.NewDisqueCluster(config)
	if err != nil {
		return nil, err
	}
	producer := &Magi{
		APIVersion:   MagiAPIVersion,
		dqCluster:    dqCluster,
		isProcessing: false,
	}
	return producer, nil
}

// Consumer creates a Magi instance that acts as a consumer
func Consumer(dqConfig *cluster.DisqueClusterConfig, rConfig *cluster.RedisClusterConfig) (*Magi, error) {
	dqCluster, err := cluster.NewDisqueCluster(dqConfig)
	if err != nil {
		return nil, err
	}
	rCluster := cluster.NewRedisCluster(rConfig)
	consumer := &Magi{
		APIVersion:     MagiAPIVersion,
		dqCluster:      dqCluster,
		rCluster:       rCluster,
		isProcessing:   false,
		processors:     make(map[string]*Processor),
		processControl: make(chan string, 1),
	}
	return consumer, nil
}

// Close terminates all connections from the Magi instance
func (m *Magi) Close() error {
	if m.dqCluster != nil {
		err := m.dqCluster.Close()
		if err != nil {
			return err
		}
	}
	if m.rCluster != nil {
		err := m.rCluster.Close()
		if err != nil {
			return err
		}
	}
	if m.isProcessing {
		m.processControl <- MagiProcessCommandStop
	}
	return nil
}

/**
 * Producer methods
 */

// AddJob adds a job to the queue
func (m *Magi) AddJob(queueName string, body string, ETA time.Time, config *cluster.DisqueOpConfig) (*job.Job, error) {
	_job, err := job.Add(m.dqCluster, queueName, body, ETA, config)
	return _job, err
}

// GetJob tries to get the details about a job
func (m *Magi) GetJob(id string) (*job.Job, error) {
	details, err := m.dqCluster.Get(id)
	if err != nil {
		if err.Error() == "no data available" {
			return nil, nil
		}
		return nil, err
	}
	_job, err := job.FromDetails(details)
	return _job, err
}

// DeleteJob removes the job from the disque cluster
func (m *Magi) DeleteJob(id string) (bool, error) {
	err := m.dqCluster.Ack(id)
	if err != nil {
		return false, err
	}
	return true, nil
}

/**
 * Consumer methods
 */

// Processor is an interface that all job processor should implement
type Processor interface {
	Process(*job.Job) (interface{}, error)
	ShouldAutoRenew(*job.Job) bool
}

// Register adds a processor for a queue
func (m *Magi) Register(queueName string, processor Processor) {
	m.processors[queueName] = &processor
}

// Process starts the job processing procedure
func (m *Magi) Process(queueName string) {
	m.isProcessing = true
	for {
		select {
		case command := <-m.processControl:
			if command == MagiProcessCommandStop {
				return
			}
		default:
			m.dqCluster.Chain()
			job, err := m.dqCluster.Fetch(queueName, nil)
			if err != nil {
				if err.Error() != "no data available" {
					fmt.Println("Error:", err)
				}
			} else {
				m.process(queueName, job.ID)
			}
			m.dqCluster.Unchain()
		}
	}
}

// IsProcessing returns whether it is currently processing jobs
func (m *Magi) IsProcessing() bool {
	return m.isProcessing
}

// ErrDisqueJobWaitFailed is the error for failing to wait on a long processing job
var ErrDisqueJobWaitFailed = errors.New("Disque Error: fail to wait on a job!")

func (m *Magi) process(queueName string, id string) {
	var _lock *lock.Lock
	// Catch panics
	defer func() {
		if err := recover(); err != nil {
			err, ok := err.(error)
			if ok && err.Error() == lock.ErrLockLost.Error() {
				// Lock is lost, release remaining lock segments
				_lock.Release()
			} else {
				panic(err)
			}
		}
	}()
	// Check if the processor is available
	processor, exists := m.processors[queueName]
	if !exists {
		return
	}
	// Get job details
	_job, err := m.GetJob(id)
	if err != nil {
		return
	}
	// Acquire lock
	_lock = lock.CreateLock(m.rCluster, id)
	result, err := _lock.Get((*processor).ShouldAutoRenew(_job))
	// If lock cannot be acquired, return and do not acknowledge
	if err != nil {
		return
	}
	if !result {
		return
	}
	// Start the auto wait extension for the job in queue
	control := make(chan bool, 1)
	_job.IsProcessing = true
	go m.autoWait(_job, &control)
	// Process the job
	(*processor).Process(_job)
	// Stop the auto wait extension
	_job.IsProcessing = false
	control <- true
	// Ack the job
	err = m.dqCluster.Ack(id)
	if err != nil {
		return
	}
	if !result {
		return
	}
	// Release the lock
	result, err = _lock.Release()
	if err != nil {
		return
	}
	if !result {
		return
	}
	return
}

func (m *Magi) autoWait(job *job.Job, control *chan bool) {
	start := time.Now()
	for {
		select {
		case command := <-*control:
			if command {
				return
			}
		default:
			if !job.IsProcessing {
				return
			}
			// Check if a wait command is needed
			elapse := float64(time.Now().Sub(start))
			threshold := float64(job.Raw.Retry) * 0.5
			if elapse >= threshold {
				// Issue wait
				err := m.dqCluster.Wait(job.ID)
				if err != nil {
					fmt.Println(err)
					panic(ErrDisqueJobWaitFailed)
				}
				// Reset ticker
				start = time.Now()
			}
			time.Sleep(time.Millisecond)
		}
	}
}
