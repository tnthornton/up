// Copyright 2021 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package diff

import (
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/pterm/pterm"
	diffv3 "github.com/r3labs/diff/v3"
	"golang.org/x/exp/maps"

	spacesv1alpha1 "github.com/upbound/up-sdk-go/apis/spaces/v1alpha1"
)

const (
	// changeUpdateFmt denotes an updated resource or field.
	changeUpdateFmt = "%s[~] %s\n"

	// changeCreateFmt denotes a created resource or field.
	changeCreateFmt = "%s[+] %s\n"

	// changeDeleteFmt denotes a deleted resource or field.
	changeDeleteFmt = "%s[-] %s\n"
)

const (
	// changeColorUpdate is the color to use when displaying an updated field.
	changeColorUpdate = pterm.FgYellow

	// changeColorCreated is the color to use when displaying an created field.
	changeColorCreate = pterm.FgGreen

	// changeColorDelete is the color to use when displaying an deleted field.
	changeColorDelete = pterm.FgRed
)

const (
	// changeSummaryFmt is the format for the printed line that summarizes the
	// results of the simulation.
	changeSummaryFmt = "Simulation: %s resources added, %s resources changed, %s resources deleted"
)

const (
	treeSymbolI  = " │ "
	treeSymbolT  = " ├─"
	treeSymbolL  = " └─"
	indentSymbol = "   "
)

type ResourceDiff struct {
	SimulationChange spacesv1alpha1.SimulationChange
	Diff             diffv3.Changelog
}

var _ diffWriter = &prettyPrintWriter{}

type diffWriter interface {
	Write(resources []ResourceDiff) error
}

// prettyPrintWriter implements diffWriter, writing its responses to a buffer that can
// be sent to stdout.
type prettyPrintWriter struct {
	w io.Writer
}

// getLoggedOutputByType returns the value that should be logged by the writer
// depending on the type.
func (p *prettyPrintWriter) getLoggedOutputByType(value any) string {
	if value == nil {
		return "<nil>"
	}

	t := reflect.TypeOf(value)
	switch t.Kind() { //nolint:exhaustive
	case reflect.String:
		return fmt.Sprintf("%q", value)
	// todo(redbackthomson): Handle pretty printing maps, arrays and interfaces
	// more nicely
	case reflect.Map:
		fallthrough
	case reflect.Array:
		fallthrough
	case reflect.Interface:
		fallthrough
	default:
		return fmt.Sprintf("%v", value)
	}
}

// printFieldUpdate prints the before and after values of a given field.
func (p *prettyPrintWriter) printFieldUpdate(prefix string, change diffv3.Change) {
	from := p.getLoggedOutputByType(change.From)
	to := p.getLoggedOutputByType(change.To)
	fmt.Fprintf(p.w, changeDeleteFmt, prefix+treeSymbolT, changeColorDelete.Sprint(from))
	fmt.Fprintf(p.w, changeCreateFmt, prefix+treeSymbolL, changeColorCreate.Sprint(to))
}

// printNode recursively writes each value a diff tree node, prefixing values
// with table symbols and indentation.
func (p *prettyPrintWriter) printNode(prefix string, isLast bool, path []string, node *DiffTreeNode[treeValue]) {
	children := maps.Values(node.children)
	path = append(path, node.key)

	// condense path and continue
	if node.numChildren == 1 {
		p.printNode(prefix, isLast, path, children[0])
		return
	}

	// write the branching path name
	namePrefix := prefix + treeSymbolT
	if isLast {
		namePrefix = prefix + treeSymbolL
	}
	fmt.Fprintf(p.w, changeUpdateFmt, namePrefix, formatFieldPath(path))

	// write the field diff
	if node.IsLeaf() {
		childPrefix := treeSymbolI
		if isLast {
			childPrefix = indentSymbol
		}
		p.printFieldUpdate(prefix+childPrefix, node.value)
		return
	}

	// recursively write the children
	childPrefix := prefix + treeSymbolI
	if isLast {
		childPrefix = prefix + indentSymbol
	}
	for i, child := range children {
		lastChild := (i == len(children)-1)
		p.printNode(childPrefix, lastChild, []string{}, child)
	}
}

// Write writes the diffed resources as a pretty-printed table out to the
// associated buffer.
func (p *prettyPrintWriter) Write(resources []ResourceDiff) error {
	p.writeSummary(resources)

	// todo(redbackthomson): Sort by gvk, name and change type (delete, create,
	// update)
	for _, change := range resources {
		ref := change.SimulationChange.ObjectReference

		switch change.SimulationChange.Change { //nolint:exhaustive
		case spacesv1alpha1.SimulationChangeTypeCreate:
			fmt.Fprintf(p.w, changeCreateFmt, "", changeColorCreate.Sprint(formatObjectReference(ref)))
			continue
		case spacesv1alpha1.SimulationChangeTypeDelete:
			fmt.Fprintf(p.w, changeDeleteFmt, "", changeColorDelete.Sprint(formatObjectReference(ref)))
			continue
		}

		fmt.Fprintf(p.w, changeUpdateFmt, "", changeColorUpdate.Sprint(formatObjectReference(ref)))

		// hide any changes to secrets
		if change.SimulationChange.ObjectReference.Kind == "Secret" &&
			change.SimulationChange.ObjectReference.APIVersion == "v1" {
			continue
		}

		root := BuildDiffTree(change)
		for i, child := range maps.Values(root.children) {
			p.printNode("", i == (len(root.children)-1), []string{""}, child)
		}
	}
	return nil
}

// writeSummary writes a summarised version of the differences to the associated
// buffer.
func (p *prettyPrintWriter) writeSummary(resources []ResourceDiff) {
	updated, created, deleted := 0, 0, 0
	for _, res := range resources {
		switch res.SimulationChange.Change {
		case spacesv1alpha1.SimulationChangeTypeCreate:
			created += 1
		case spacesv1alpha1.SimulationChangeTypeDelete:
			deleted += 1
		case spacesv1alpha1.SimulationChangeTypeUpdate:
			updated += 1
		case spacesv1alpha1.SimulationChangeTypeUnknown:
		}
	}

	fmt.Fprintf(p.w, changeSummaryFmt, changeColorCreate.Sprint(created), changeColorUpdate.Sprint(updated), changeColorDelete.Sprint(deleted))
	fmt.Fprintf(p.w, "\n\n")
}

// formatFieldPath returns a pretty-printed a field path.
func formatFieldPath(path []string) string {
	return strings.TrimPrefix(strings.Join(path, "."), ".")
}

// formatObjectReference returns a pretty-printed object reference.
func formatObjectReference(ref spacesv1alpha1.ChangedObjectReference) string {
	name := ref.Name
	if ref.Namespace != nil {
		name = *ref.Namespace + "/" + name
	}

	return fmt.Sprintf("%s.%s %s", ref.Kind, ref.APIVersion, name)
}

// NewPrettyPrintWriter creates a new print writer that, when calling `Write()`, will
// output a pretty-printed table to the writer.
func NewPrettyPrintWriter(w io.Writer) *prettyPrintWriter {
	return &prettyPrintWriter{
		w: w,
	}
}
