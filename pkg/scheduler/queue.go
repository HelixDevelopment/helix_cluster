package scheduler

import (
	"container/heap"
	"sync"
)

// queueItem wraps a Job for priority-queue ordering.
type queueItem struct {
	job      *Job
	index    int // position in the heap slice
	enqueued int // tie-breaker for FIFO within same priority
}

// priorityQueue implements heap.Interface.
type priorityQueue []*queueItem

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool {
	if pq[i].job.Priority == pq[j].job.Priority {
		return pq[i].enqueued < pq[j].enqueued
	}
	return pq[i].job.Priority > pq[j].job.Priority
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	item := x.(*queueItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	item.index = -1
	*pq = old[:n-1]
	return item
}

// Queue is a thread-safe priority scheduling queue.
type Queue struct {
	mu       sync.Mutex
	pq       priorityQueue
	seq      int
	itemsMap map[string]*queueItem // for O(1) lookups
}

// NewQueue creates an empty scheduling queue.
func NewQueue() *Queue {
	return &Queue{
		pq:       make(priorityQueue, 0),
		itemsMap: make(map[string]*queueItem),
	}
}

// Add inserts a job into the queue.
func (q *Queue) Add(job *Job) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.itemsMap[job.ID]; exists {
		return
	}
	q.seq++
	item := &queueItem{job: job, enqueued: q.seq}
	heap.Push(&q.pq, item)
	q.itemsMap[job.ID] = item
}

// Pop removes and returns the highest-priority job.
func (q *Queue) Pop() *Job {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.pq) == 0 {
		return nil
	}
	item := heap.Pop(&q.pq).(*queueItem)
	delete(q.itemsMap, item.job.ID)
	return item.job
}

// Peek returns the highest-priority job without removing it.
func (q *Queue) Peek() *Job {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.pq) == 0 {
		return nil
	}
	return q.pq[0].job
}

// Len returns the number of jobs in the queue.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pq)
}
