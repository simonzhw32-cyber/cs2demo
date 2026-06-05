package orchestrator

import (
	"context"
	"log"
	"sync"

	"github.com/cs2demo/platform/internal/analyzer"
	"github.com/cs2demo/platform/internal/domain"
	"github.com/cs2demo/platform/internal/parser"
	"github.com/cs2demo/platform/internal/storage"
)

type Job struct {
	DemoID     string
	FilePath   string
	TargetUser string
}

type Orchestrator struct {
	store    *storage.Store
	parser   *parser.Parser
	analyzer *analyzer.Analyzer
	jobs     chan Job
	wg       sync.WaitGroup
}

func New(store *storage.Store, p *parser.Parser, a *analyzer.Analyzer, workers int) *Orchestrator {
	if workers <= 0 {
		workers = 2
	}
	o := &Orchestrator{
		store:    store,
		parser:   p,
		analyzer: a,
		jobs:     make(chan Job, 32),
	}
	for i := 0; i < workers; i++ {
		o.wg.Add(1)
		go o.worker(i)
	}
	return o
}

func (o *Orchestrator) Enqueue(j Job) { o.jobs <- j }

func (o *Orchestrator) Shutdown() {
	close(o.jobs)
	o.wg.Wait()
}

func (o *Orchestrator) worker(id int) {
	defer o.wg.Done()
	for j := range o.jobs {
		o.run(j)
	}
	log.Printf("worker %d stopped", id)
}

func (o *Orchestrator) run(j Job) {
	ctx := context.Background()
	log.Printf("[job %s] start parsing %s", j.DemoID, j.FilePath)

	if err := o.store.UpdateStatus(ctx, j.DemoID, domain.StatusParsing, ""); err != nil {
		log.Printf("[job %s] status update failed: %v", j.DemoID, err)
	}

	stats, err := o.parser.Parse(j.FilePath, j.TargetUser)
	if err != nil {
		if tnf, ok := parser.IsTargetNotFound(err); ok {
			log.Printf("[job %s] target not found, candidates=%v", j.DemoID, tnf.Candidates)
			_ = o.store.UpdateFailureWithCandidates(ctx, j.DemoID,
				"目标玩家未匹配。该 demo 中有以下玩家可选，请重新提交。", tnf.Candidates)
			return
		}
		log.Printf("[job %s] parse failed: %v", j.DemoID, err)
		_ = o.store.UpdateStatus(ctx, j.DemoID, domain.StatusFailed, "parse: "+err.Error())
		return
	}
	if err := o.store.SaveStats(ctx, j.DemoID, stats); err != nil {
		log.Printf("[job %s] save stats failed: %v", j.DemoID, err)
		_ = o.store.UpdateStatus(ctx, j.DemoID, domain.StatusFailed, "save stats: "+err.Error())
		return
	}

	if err := o.store.UpdateStatus(ctx, j.DemoID, domain.StatusAnalyzing, ""); err != nil {
		log.Printf("[job %s] status update failed: %v", j.DemoID, err)
	}

	report, err := o.analyzer.Analyze(ctx, j.DemoID, stats)
	analysisWarning := ""
	if err != nil {
		log.Printf("[job %s] analyze warning: %v (will save fallback report)", j.DemoID, err)
		analysisWarning = "analyze: " + err.Error()
	}
	if err := o.store.SaveReport(ctx, j.DemoID, report); err != nil {
		log.Printf("[job %s] save report failed: %v", j.DemoID, err)
		_ = o.store.UpdateStatus(ctx, j.DemoID, domain.StatusFailed, "save report: "+err.Error())
		return
	}

	_ = o.store.UpdateStatus(ctx, j.DemoID, domain.StatusDone, analysisWarning)
	log.Printf("[job %s] done", j.DemoID)
}
