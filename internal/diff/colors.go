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
	"fmt"

	"github.com/pterm/pterm"
)

const (
	// changeColorCreated is the color to use when displaying an created field.
	changeColorCreate = pterm.FgGreen

	// changeColorUpdate is the color to use when displaying an updated field.
	changeColorUpdate = pterm.FgYellow

	// changeColorDelete is the color to use when displaying an deleted field.
	changeColorDelete = pterm.FgRed
)

// outputStyles defines how the output will be styled depending on the change
// type.
type outputStyles interface {
	Create(...any) string
	Update(...any) string
	Delete(...any) string
}

var _ outputStyles = &termColors{}

// termColors formats the output with terminal foreground colors.
type termColors struct {
	create pterm.Color
	update pterm.Color
	delete pterm.Color
}

func (c termColors) Create(v ...any) string {
	return c.create.Sprint(v...)
}

func (c termColors) Update(v ...any) string {
	return c.update.Sprint(v...)
}

func (c termColors) Delete(v ...any) string {
	return c.delete.Sprint(v...)
}

func NewDefaultTermColors() termColors {
	return termColors{
		create: changeColorCreate,
		update: changeColorUpdate,
		delete: changeColorDelete,
	}
}

var _ outputStyles = &noColors{}

type noColors struct{}

func (noColors) Create(v ...any) string {
	return fmt.Sprint(v...)
}

func (noColors) Update(v ...any) string {
	return fmt.Sprint(v...)
}

func (noColors) Delete(v ...any) string {
	return fmt.Sprint(v...)
}
