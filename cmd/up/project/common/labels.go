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

package common

import (
	"fmt"
	"reflect"

	"github.com/upbound/up/internal/version"
)

// ImageLabels returns the image labels that should be applied to images
// generated by the given command struct.
func ImageLabels(cmd any) map[string]string {
	return map[string]string{
		"io.upbound.up.userAgent": version.UserAgent(),
		"io.upbound.up.buildCmd":  getCmdOptions(cmd),
	}
}

// getCmdOptions generates a string of all command-line options from the command
// struct.
func getCmdOptions(cmd any) string {
	v := reflect.ValueOf(cmd)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	typeOfCmd := v.Type()

	var options string

	for i := 0; i < v.NumField(); i++ {
		field := typeOfCmd.Field(i)
		value := v.Field(i)
		if !value.CanInterface() {
			continue
		}
		if options != "" {
			options += ", "
		}
		options += fmt.Sprintf("%s: %v", field.Name, value.Interface())
	}

	return options
}