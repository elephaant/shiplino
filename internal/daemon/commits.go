package daemon

import (
	"context"
	"path/filepath"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

const maxCommitFiles = 50

// checkCommits turns new commits in watched worktrees into git.commit
// events on the sessions that produced them. Called with d.mu held.
func (d *Daemon) checkCommits(ctx context.Context) error {
	if d.git == nil {
		return nil
	}
	commits := d.git.Check()
	if len(commits) == 0 {
		return nil
	}
	var events []model.Event
	for _, c := range commits {
		attrs := d.eng.AttributeCommit(engine.CommitInfo{When: c.When, Worktree: c.Worktree, Files: c.Files})
		if len(attrs) == 0 {
			continue
		}
		auth := d.commitAuthorship(ctx, c, attrs)
		for _, a := range attrs {
			s := d.eng.Get(a.SessionID)
			if s == nil {
				continue
			}
			files := make([]string, 0, min(len(c.Files), maxCommitFiles))
			for _, f := range c.Files {
				if len(files) == maxCommitFiles {
					break
				}
				if rel, err := filepath.Rel(c.Worktree, f); err == nil {
					files = append(files, filepath.ToSlash(rel))
				}
			}
			data := map[string]any{
				"sha": c.SHA, "message": c.Subject, "author": c.Author, "files": files, "files_changed": len(c.Files),
				"lines_added": c.LinesAdded, "lines_removed": c.LinesRemoved, "attribution": a.Confidence,
			}
			auth.data(data)
			events = append(events, model.Event{
				ID: model.NewULID(c.When), V: model.SchemaVersion, TS: c.When.UTC(), ReceivedAt: c.When.UTC(),
				Kind: model.KindGitCommit, Agent: model.Agent{Name: s.Agent}, Collector: model.CollectorGit,
				User: d.user, SessionID: s.ID, ActorID: s.ID,
				Project:  &model.Project{CWD: c.Worktree, RepoRoot: c.Worktree},
				Data:     data,
				DedupKey: s.ID + ":git:" + c.SHA,
			})
		}
	}
	if len(events) == 0 {
		return nil
	}
	for i := range events {
		d.redactor.Event(&events[i], d.level) // before anything touches disk
	}
	return d.commit(ctx, events, nil)
}
