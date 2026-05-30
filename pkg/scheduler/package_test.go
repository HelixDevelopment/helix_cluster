package scheduler

import (
	"fmt"
	"sync"
	"testing"
)

// ---------- types tests ----------

func TestJobStatusConstants(t *testing.T) {
	if JobStatusPending != "pending" {
		t.Error("unexpected pending status")
	}
	if JobStatusScheduled != "scheduled" {
		t.Error("unexpected scheduled status")
	}
}

func TestJobStatusMutation(t *testing.T) {
	j := &Job{ID: "j1", Status: JobStatusPending}
	j.Status = JobStatusRunning
	if j.Status != JobStatusRunning {
		t.Error("status mutation failed")
	}
}

// ---------- queue tests ----------

func TestQueueAddAndLen(t *testing.T) {
	q := NewQueue()
	q.Add(&Job{ID: "a", Priority: 1})
	if q.Len() != 1 {
		t.Fatalf("expected len 1, got %d", q.Len())
	}
}

func TestQueuePriorityOrdering(t *testing.T) {
	q := NewQueue()
	q.Add(&Job{ID: "low", Priority: 1})
	q.Add(&Job{ID: "high", Priority: 10})
	q.Add(&Job{ID: "mid", Priority: 5})

	first := q.Pop()
	if first.ID != "high" {
		t.Fatalf("expected high priority first, got %s", first.ID)
	}
	second := q.Pop()
	if second.ID != "mid" {
		t.Fatalf("expected mid priority second, got %s", second.ID)
	}
	third := q.Pop()
	if third.ID != "low" {
		t.Fatalf("expected low priority third, got %s", third.ID)
	}
}

func TestQueueFIFOWithinPriority(t *testing.T) {
	q := NewQueue()
	q.Add(&Job{ID: "first", Priority: 5})
	q.Add(&Job{ID: "second", Priority: 5})

	if q.Pop().ID != "first" {
		t.Fatal("expected FIFO within same priority")
	}
	if q.Pop().ID != "second" {
		t.Fatal("expected FIFO within same priority")
	}
}

func TestQueuePeek(t *testing.T) {
	q := NewQueue()
	q.Add(&Job{ID: "x", Priority: 5})
	if q.Peek().ID != "x" {
		t.Fatal("peek should return head")
	}
	if q.Len() != 1 {
		t.Fatal("peek should not remove item")
	}
}

func TestQueueDuplicateAddIgnored(t *testing.T) {
	q := NewQueue()
	q.Add(&Job{ID: "dup", Priority: 5})
	q.Add(&Job{ID: "dup", Priority: 5})
	if q.Len() != 1 {
		t.Fatalf("expected duplicate add to be ignored, got len %d", q.Len())
	}
}

func TestQueueConcurrentAccess(t *testing.T) {
	q := NewQueue()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q.Add(&Job{ID: fmt.Sprintf("job-%d", i), Priority: i})
		}(i)
	}
	wg.Wait()
	if q.Len() != 100 {
		t.Fatalf("expected 100 items, got %d", q.Len())
	}
}

// ---------- plugin tests ----------

func TestNodeResourcesFitFilter(t *testing.T) {
	plugin := &NodeResourcesFit{}
	job := &Job{ID: "j1", Resources: Resources{CPU: 2, Memory: 1024, GPU: 1}}
	node := &Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 2048, GPU: 2}}

	if !plugin.Filter(job, node) {
		t.Fatal("expected node to pass filter")
	}
}

func TestNodeResourcesFitFilterInsufficient(t *testing.T) {
	plugin := &NodeResourcesFit{}
	job := &Job{ID: "j1", Resources: Resources{CPU: 8, Memory: 1024, GPU: 0}}
	node := &Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 2048, GPU: 0}}

	if plugin.Filter(job, node) {
		t.Fatal("expected node to fail filter")
	}
}

func TestNodeResourcesFitBindDeductsResources(t *testing.T) {
	plugin := &NodeResourcesFit{}
	job := &Job{ID: "j1", Resources: Resources{CPU: 1, Memory: 512, GPU: 1}}
	node := &Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 2048, GPU: 2}}

	if !plugin.Bind(job, node) {
		t.Fatal("bind should succeed")
	}
	if node.AvailableResources.CPU != 3 {
		t.Fatalf("expected CPU 3, got %f", node.AvailableResources.CPU)
	}
	if node.AvailableResources.Memory != 1536 {
		t.Fatalf("expected Memory 1536, got %d", node.AvailableResources.Memory)
	}
	if node.AvailableResources.GPU != 1 {
		t.Fatalf("expected GPU 1, got %d", node.AvailableResources.GPU)
	}
}

func TestCapabilityMatchFilter(t *testing.T) {
	plugin := &CapabilityMatch{}
	job := &Job{
		ID:     "j1",
		Labels: map[string]string{"require_zone": "us-east"},
	}
	node := &Node{ID: "n1", Labels: map[string]string{"zone": "us-east"}}

	if !plugin.Filter(job, node) {
		t.Fatal("expected capability match")
	}
}

func TestCapabilityMatchFilterMissing(t *testing.T) {
	plugin := &CapabilityMatch{}
	job := &Job{
		ID:     "j1",
		Labels: map[string]string{"require_zone": "us-east"},
	}
	node := &Node{ID: "n1", Labels: map[string]string{"zone": "us-west"}}

	if plugin.Filter(job, node) {
		t.Fatal("expected capability mismatch")
	}
}

func TestCapabilityMatchScore(t *testing.T) {
	plugin := &CapabilityMatch{}
	job := &Job{
		ID:     "j1",
		Labels: map[string]string{"prefer_zone": "us-east"},
	}
	node := &Node{ID: "n1", Labels: map[string]string{"zone": "us-east"}}

	score := plugin.Score(job, node)
	if score <= 0 {
		t.Fatalf("expected positive score for preference match, got %d", score)
	}
}

// ---------- scheduler tests ----------

func TestSchedulerRegisterNode(t *testing.T) {
	s := NewScheduler()
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4}})
	if s.NodeCount() != 1 {
		t.Fatalf("expected 1 node, got %d", s.NodeCount())
	}
}

func TestSchedulerUnregisterNode(t *testing.T) {
	s := NewScheduler()
	s.RegisterNode(&Node{ID: "n1"})
	s.UnregisterNode("n1")
	if s.NodeCount() != 0 {
		t.Fatalf("expected 0 nodes, got %d", s.NodeCount())
	}
}

func TestSchedulerScheduleJob(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096, GPU: 0}})

	job := &Job{ID: "j1", Command: "echo hello", Resources: Resources{CPU: 1, Memory: 1024}, Priority: 5}
	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.AssignedNode != "n1" {
		t.Fatalf("expected node n1, got %s", res.AssignedNode)
	}
	if res.Status != JobStatusScheduled {
		t.Fatalf("expected scheduled status, got %s", res.Status)
	}
}

func TestSchedulerScheduleNoNode(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})

	job := &Job{ID: "j1", Resources: Resources{CPU: 1, Memory: 1024}}
	_, err := s.Schedule(job)
	if err == nil {
		t.Fatal("expected error when no nodes available")
	}
}

func TestSchedulerScheduleInsufficientResources(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 0.5, Memory: 512}})

	job := &Job{ID: "j1", Resources: Resources{CPU: 2, Memory: 1024}}
	_, err := s.Schedule(job)
	if err == nil {
		t.Fatal("expected error for insufficient resources")
	}
}

func TestSchedulerScheduleWithCapabilityMatch(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&CapabilityMatch{})
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}, Labels: map[string]string{"zone": "us-east"}})
	s.RegisterNode(&Node{ID: "n2", AvailableResources: Resources{CPU: 4, Memory: 4096}, Labels: map[string]string{"zone": "us-west"}})

	job := &Job{ID: "j1", Resources: Resources{CPU: 1, Memory: 1024}, Labels: map[string]string{"require_zone": "us-east"}}
	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.AssignedNode != "n1" {
		t.Fatalf("expected n1, got %s", res.AssignedNode)
	}
}

func TestSchedulerSchedulePicksBestScore(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&LoadAware{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 2, Memory: 2048}})
	s.RegisterNode(&Node{ID: "n2", AvailableResources: Resources{CPU: 8, Memory: 8192}})

	job := &Job{ID: "j1", Resources: Resources{CPU: 1, Memory: 1024}}
	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// LoadAware prefers more free resources, so n2 should win.
	if res.AssignedNode != "n2" {
		t.Fatalf("expected n2 (more resources), got %s", res.AssignedNode)
	}
}

func TestSchedulerVersionIncrement(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	v1 := s.Version()
	job := &Job{ID: "j1", Resources: Resources{CPU: 1, Memory: 1024}}
	s.Schedule(job)
	v2 := s.Version()
	if v2 != v1+1 {
		t.Fatalf("expected version increment, %d -> %d", v1, v2)
	}
}

func TestSchedulerOptimisticConcurrencyConflict(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	job1 := &Job{ID: "j1", Resources: Resources{CPU: 1, Memory: 1024}}
	job2 := &Job{ID: "j2", Resources: Resources{CPU: 1, Memory: 1024}}

	v := s.Version()
	_, err := s.ScheduleOptimistic(job1, v)
	if err != nil {
		t.Fatalf("first optimistic schedule failed: %v", err)
	}

	// stale version should conflict
	_, err = s.ScheduleOptimistic(job2, v)
	if err == nil {
		t.Fatal("expected optimistic concurrency conflict")
	}
}

func TestSchedulerOptimisticConcurrencySuccess(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	job := &Job{ID: "j1", Resources: Resources{CPU: 1, Memory: 1024}}
	v := s.Version()
	res, err := s.ScheduleOptimistic(job, v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.AssignedNode != "n1" {
		t.Fatalf("expected n1, got %s", res.AssignedNode)
	}
}

func TestSchedulerConcurrentSchedules(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 100, Memory: 102400}})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job := &Job{ID: string(rune('a' + i%26)), Resources: Resources{CPU: 1, Memory: 1024}}
			s.Schedule(job)
		}(i)
	}
	wg.Wait()

	n, _ := s.GetNode("n1")
	if n.AvailableResources.CPU != 50 {
		t.Fatalf("expected 50 CPU left, got %f", n.AvailableResources.CPU)
	}
}

func TestSchedulerAddPluginOrder(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&PrioritySort{})
	s.AddPlugin(&NodeResourcesFit{})
	if len(s.plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(s.plugins))
	}
}

func TestSchedulerGetNodeCopy(t *testing.T) {
	s := NewScheduler()
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4}, Labels: map[string]string{"k": "v"}})
	n, ok := s.GetNode("n1")
	if !ok {
		t.Fatal("expected node to exist")
	}
	n.AvailableResources.CPU = 999
	n.Labels["k"] = "mutated"

	orig, _ := s.GetNode("n1")
	if orig.AvailableResources.CPU != 4 {
		t.Fatal("GetNode should return a copy")
	}
	if orig.Labels["k"] != "v" {
		t.Fatal("GetNode should return a copy of labels")
	}
}

// ---------- mutation tests ----------

func TestMutationJobStatus(t *testing.T) {
	j := &Job{ID: "j1", Status: JobStatusPending}
	j.Status = JobStatusFailed
	if j.Status != JobStatusFailed {
		t.Fatal("mutation: status should be failed")
	}
}

func TestMutationQueuePopEmpty(t *testing.T) {
	q := NewQueue()
	if q.Pop() != nil {
		t.Fatal("mutation: pop on empty queue should return nil")
	}
}

func TestMutationQueuePeekEmpty(t *testing.T) {
	q := NewQueue()
	if q.Peek() != nil {
		t.Fatal("mutation: peek on empty queue should return nil")
	}
}

func TestMutationNodeResourcesFitNilNode(t *testing.T) {
	p := &NodeResourcesFit{}
	if p.Filter(&Job{ID: "j1"}, nil) {
		t.Fatal("mutation: nil node should fail filter")
	}
}

func TestMutationCapabilityMatchNilNode(t *testing.T) {
	p := &CapabilityMatch{}
	if p.Filter(&Job{ID: "j1", Labels: map[string]string{"require_x": "y"}}, nil) {
		t.Fatal("mutation: nil node should fail filter")
	}
}

func TestMutationLoadAwareNilNode(t *testing.T) {
	p := &LoadAware{}
	if p.Score(&Job{ID: "j1"}, nil) != 0 {
		t.Fatal("mutation: nil node should score 0")
	}
}

func TestMutationSchedulerNoPlugins(t *testing.T) {
	s := NewScheduler()
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4}})
	job := &Job{ID: "j1", Resources: Resources{CPU: 1}}
	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("mutation: no plugins means all nodes pass: %v", err)
	}
	if res.AssignedNode != "n1" {
		t.Fatalf("mutation: expected n1, got %s", res.AssignedNode)
	}
}

func TestMutationSchedulerVersionStartsAtOne(t *testing.T) {
	s := NewScheduler()
	if s.Version() != 1 {
		t.Fatalf("mutation: version should start at 1, got %d", s.Version())
	}
}

func TestMutationPrioritySortNoOp(t *testing.T) {
	p := &PrioritySort{}
	if !p.Filter(nil, nil) || p.Score(nil, nil) != 0 || !p.Bind(nil, nil) {
		t.Fatal("mutation: PrioritySort should be no-op")
	}
}

func TestMutationCapabilityMatchNoRequirements(t *testing.T) {
	p := &CapabilityMatch{}
	job := &Job{ID: "j1", Labels: map[string]string{}}
	node := &Node{ID: "n1", Labels: map[string]string{"zone": "us-east"}}
	if !p.Filter(job, node) {
		t.Fatal("mutation: no requirements should pass")
	}
}

func TestMutationNodeResourcesFitScoreZero(t *testing.T) {
	p := &NodeResourcesFit{}
	job := &Job{ID: "j1", Resources: Resources{CPU: 10, Memory: 1024}}
	node := &Node{ID: "n1", AvailableResources: Resources{CPU: 1, Memory: 512}}
	if p.Score(job, node) != 0 {
		t.Fatal("mutation: insufficient resources should score 0")
	}
}

func TestMutationBindFails(t *testing.T) {
	s := NewScheduler()
	// NodeResourcesFit will fail bind if resources insufficient after filter
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 0.1, Memory: 64}})
	job := &Job{ID: "j1", Resources: Resources{CPU: 0.1, Memory: 64}}
	// Filter passes exactly, bind deducts to zero (still passes).
	// To force bind failure we need a plugin that rejects bind.
	bindReject := &bindRejectPlugin{}
	s.AddPlugin(bindReject)
	_, err := s.Schedule(job)
	if err == nil {
		t.Fatal("mutation: bind rejection should produce error")
	}
}

type bindRejectPlugin struct{}

func (b *bindRejectPlugin) Name() string                { return "BindReject" }
func (b *bindRejectPlugin) Filter(job *Job, node *Node) bool { return true }
func (b *bindRejectPlugin) Score(job *Job, node *Node) int   { return 0 }
func (b *bindRejectPlugin) Bind(job *Job, node *Node) bool   { return false }
