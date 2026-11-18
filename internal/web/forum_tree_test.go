package web

import (
	"database/sql"
	"testing"

	"github.com/chenbenny31/plurb/internal/store"
)

func node(id, parent int64) *commentNode {
	c := &store.Comment{ID: id}
	if parent > 0 {
		c.ParentID = sql.NullInt64{Int64: parent, Valid: true}
	}
	return &commentNode{Comment: c}
}

func TestBuildTree(t *testing.T) {
	// 1 ── 2 ── 3
	// │    └─── 4
	// 5
	nodes := []*commentNode{node(1, 0), node(2, 1), node(3, 2), node(4, 2), node(5, 0)}
	roots := buildTree(nodes, 3)

	if len(roots) != 2 || roots[0].ID != 1 || roots[1].ID != 5 {
		t.Fatalf("roots = %v", roots)
	}
	one, two := roots[0], roots[0].Children[0]
	if one.Replies != 3 || two.Replies != 2 || roots[1].Replies != 0 {
		t.Errorf("reply counts: 1=%d 2=%d 5=%d", one.Replies, two.Replies, roots[1].Replies)
	}
	if !one.Open || !two.Open {
		t.Error("ancestors of the open comment should be expanded")
	}
	if roots[1].Open || two.Children[1].Open {
		t.Error("unrelated branches should stay collapsed")
	}
	if buildTree([]*commentNode{node(7, 0), node(8, 7)}, 0)[0].Open {
		t.Error("nothing should open by default")
	}
}
