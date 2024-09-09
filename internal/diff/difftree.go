// Copyright 2024 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package diff

import (
	diffv3 "github.com/r3labs/diff/v3"
)

type treeValue = diffv3.Change

// DiffTreeNode defines a prefix-tree node specifically defined to be used for
// parsing object diffs.
type DiffTreeNode[V any] struct {
	key         string
	value       V
	children    map[string]*DiffTreeNode[V]
	numChildren int
}

// IsLeaf returns true if the node is at the leaf of the tree.
func (n *DiffTreeNode[V]) IsLeaf() bool {
	return n.numChildren == 0
}

// Put inserts a value as a leaf into the tree at the given path.
func (n *DiffTreeNode[V]) Put(path []string, value V) {
	if len(path) == 0 {
		n.value = value
		return
	}
	front, rest := path[0], path[1:]

	child, exists := n.children[front]
	if exists {
		child.Put(rest, value)
		return
	}

	child = newNode[V](front)
	n.children[front] = child
	n.numChildren += 1

	child.Put(rest, value)
}

// newNode creates a new, empty node of the diff tree.
func newNode[V any](key string) *DiffTreeNode[V] {
	return &DiffTreeNode[V]{
		key:         key,
		children:    map[string]*DiffTreeNode[V]{},
		numChildren: 0,
	}
}

// BuildDiffTree builds a diff tree given a list of resource diffs, where each
// diff will represent a leaf in the tree.
func BuildDiffTree(diffs ResourceDiff) *DiffTreeNode[treeValue] {
	root := newNode[treeValue]("")

	for _, diff := range diffs.Diff {
		root.Put(diff.Path, diff)
	}

	return root
}
