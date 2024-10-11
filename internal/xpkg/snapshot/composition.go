// Copyright 2022 Upbound Inc
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

package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	verrors "k8s.io/kube-openapi/pkg/validation/errors"
	"k8s.io/kube-openapi/pkg/validation/validate"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	xpextv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"

	icomposite "github.com/crossplane/crossplane/controller/apiextensions/composite"
	icompositions "github.com/crossplane/crossplane/controller/apiextensions/compositions"

	"github.com/upbound/up/internal/xpkg"
	mxpkg "github.com/upbound/up/internal/xpkg/dep/marshaler/xpkg"
	"github.com/upbound/up/internal/xpkg/snapshot/validator"
	projectv1alpha1 "github.com/upbound/up/pkg/apis/project/v1alpha1"
)

const (
	resources = "spec.resources"

	errFmt                  = "%s (%s)"
	errInvalidValidationFmt = "invalid validation result returned for %s"
	resourceBaseFmt         = "spec.resources[%d].base.%s"

	pipelineStepFunctionNameFmt = "spec.pipeline[%d].functionRef.name"
	pipelineStepInputFmt        = "spec.pipeline[%d].input"
	pipelineStepInputFieldFmt   = "spec.pipeline[%d].input.%s"

	errIncorrectErrType = "incorrect validaton error type seen"
	errInvalidType      = "invalid type passed in, expected Unstructured"
)

// CompositionValidator defines a validator for compositions.
type CompositionValidator struct {
	s          *Snapshot
	validators []compositionValidator
}

// DefaultCompositionValidators returns a new Composition validator.
func DefaultCompositionValidators(s *Snapshot) (validator.Validator, error) {
	return &CompositionValidator{
		s: s,
		validators: []compositionValidator{
			NewPatchesValidator(s),
		},
	}, nil
}

// Validate performs validation rules on the given data input per the rules
// defined for the Validator.
func (c *CompositionValidator) Validate(ctx context.Context, data any) *validate.Result {
	errs := []error{}

	comp, err := c.marshal(data)
	if err != nil {
		return validator.Nop
	}

	compRefGVK := schema.FromAPIVersionAndKind(
		comp.Spec.CompositeTypeRef.APIVersion,
		comp.Spec.CompositeTypeRef.Kind,
	)

	r := icomposite.NewReconciler(resource.CompositeKind(compRefGVK), icomposite.WithLogger(c.s.log))
	cds, err := r.Reconcile(ctx, comp)
	if err != nil {
		// some validation errors occur during reconciliation that we want to
		// send to the end user.
		ie := &validator.Validation{
			TypeCode: validator.ErrorTypeCode,
			Message:  err.Error(),
			Name:     resources,
		}
		errs = append(errs, ie)
	}

	if len(errs) == 0 {
		for i, cd := range cds {
			for _, v := range c.validators {
				errs = append(errs, v.validate(ctx, i, cd.Resource)...)
			}
		}
	}

	errs = append(errs, c.validatePipeline(ctx, comp)...)

	return &validate.Result{
		Errors: errs,
	}
}

// validatePipeline validates a composition's pipeline.
func (c *CompositionValidator) validatePipeline(ctx context.Context, comp *xpextv1.Composition) []error {
	var errs []error

	if comp.Spec.Mode == nil || *comp.Spec.Mode != xpextv1.CompositionModePipeline {
		// No pipeline to validate since the composition uses P&T mode.
		return nil
	}

	errs = append(errs, c.validatePipelineFunctionRefs(comp)...)
	errs = append(errs, c.validatePipelineFunctionInputs(ctx, comp)...)

	return errs
}

// validatePipelineFunctionRefs validates that each pipeline step refers to a
// function that is a dependency of the package.
func (c *CompositionValidator) validatePipelineFunctionRefs(comp *xpextv1.Composition) []error {
	var errs []error

	deps, err := c.s.wsview.Meta().DependsOn()
	if err != nil {
		errs = append(errs, errors.Wrap(err, "failed to get dependencies"))
		return errs
	}
	// Create embedded function dependencies so we know the valid embedded
	// function names below.
	embeddedFns, err := c.collectFunctionDeps()
	if err != nil {
		errs = append(errs, err)
	}
	deps = append(deps, embeddedFns...)

	// Figure out the valid function names based on the dependencies.
	functionDeps := make(map[string]bool)
	for _, dep := range deps {
		if dep.Type != v1beta1.FunctionPackageType {
			continue
		}
		reg, err := name.NewRepository(dep.Package)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "dependency %q has invalid registry", dep.Package))
			continue
		}
		repo := reg.RepositoryStr()
		functionDeps[xpkg.ToDNSLabel(repo)] = true
	}

	// Find any pipeline steps using an unknown function.
	for i, step := range comp.Spec.Pipeline {
		if !functionDeps[step.FunctionRef.Name] {
			errs = append(errs, &validator.Validation{
				TypeCode: validator.WarningTypeCode,
				Message:  fmt.Sprintf("package does not depend on function %q", step.FunctionRef.Name),
				Name:     fmt.Sprintf(pipelineStepFunctionNameFmt, i),
			})
		}
	}

	return errs
}

func (c *CompositionValidator) collectFunctionDeps() ([]v1beta1.Dependency, error) {
	proj, isProj := c.s.wsview.Meta().Object().(*projectv1alpha1.Project)
	if !isProj {
		return nil, nil
	}

	functionsDir := "functions"
	if proj.Spec.Paths != nil && proj.Spec.Paths.Functions != "" {
		functionsDir = proj.Spec.Paths.Functions
	}
	functionsPath := filepath.Join(c.s.wsview.MetaLocation(), functionsDir)
	projRepo, err := name.NewRepository(proj.Spec.Repository)
	if err != nil {
		return nil, err
	}

	infos, err := afero.ReadDir(c.s.w.Filesystem(), functionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	deps := make([]v1beta1.Dependency, len(infos))
	for i, info := range infos {
		fnRepo := projRepo.Registry.Repo(projRepo.RepositoryStr() + "_" + info.Name())
		deps[i] = v1beta1.Dependency{
			Package: fnRepo.String(),
			Type:    v1beta1.FunctionPackageType,
		}
	}

	return deps, nil
}

// validatePipelineFunctionInputs validates that each pipeline step's input is
// of the correct type for the function it uses.
func (c *CompositionValidator) validatePipelineFunctionInputs(ctx context.Context, comp *xpextv1.Composition) []error { //nolint:gocyclo
	var errs []error

	for stepIdx, step := range comp.Spec.Pipeline {
		if step.Input == nil {
			continue
		}

		crds, found := c.inputCRDsForFunction(step.FunctionRef.Name)
		if !found {
			continue
		}
		if len(crds) == 0 {
			errs = append(errs, &validator.Validation{
				TypeCode: validator.WarningTypeCode,
				Message:  fmt.Sprintf("function %q does not take input", step.FunctionRef.Name),
				Name:     fmt.Sprintf(pipelineStepInputFmt, stepIdx),
			})
			continue
		}

		vals := make(map[schema.GroupVersionKind]*validator.ObjectValidator)
		for _, crd := range crds {
			_ = validatorsFromV1CRD(crd, vals)
		}
		crdKinds := make([]string, 0, len(vals))
		for kind := range vals {
			crdKinds = append(crdKinds, kind.String())
		}

		var u unstructured.Unstructured
		err := json.Unmarshal(step.Input.Raw, &u)
		if err != nil {
			errs = append(errs, &validator.Validation{
				TypeCode: validator.WarningTypeCode,
				Message:  err.Error(),
				Name:     fmt.Sprintf(pipelineStepInputFmt, stepIdx),
			})
			continue
		}
		val, ok := vals[u.GroupVersionKind()]
		if !ok {
			errs = append(errs, &validator.Validation{
				TypeCode: validator.WarningTypeCode,
				Message:  fmt.Sprintf("incorrect input type for step %q; valid inputs: %v", step.Step, crdKinds),
				Name:     fmt.Sprintf(pipelineStepInputFieldFmt, stepIdx, "apiVersion"),
			})
			continue
		}
		res := val.Validate(ctx, &u)
		for _, e := range append(res.Errors, res.Warnings...) {
			var ve *verrors.Validation
			if !errors.As(e, &ve) {
				return []error{errors.New(errIncorrectErrType)}
			}
			ie := &validator.Validation{
				TypeCode: ve.Code(),
				Message:  fmt.Sprintf(errFmt, ve.Error(), u.GroupVersionKind()),
				Name:     fmt.Sprintf(pipelineStepInputFieldFmt, stepIdx, ve.Name),
			}
			errs = append(errs, ie)
		}
	}

	return errs
}

// inputCRDsForFunction returns all the CRDs included in the package for the
// function with the given name. Note that the name here is not the name of the
// function package, but the name constructed by the package manager when the
// function is installed as a dependency.
func (c *CompositionValidator) inputCRDsForFunction(name string) ([]*apiextv1.CustomResourceDefinition, bool) {
	possibleRepos := possibleReposForFunction(name)

	var pkg *mxpkg.ParsedPackage
	for _, repo := range possibleRepos {
		got := c.s.Package(repo)
		if got == nil || got.Type() != v1beta1.FunctionPackageType {
			continue
		}
		pkg = got
		break
	}
	if pkg == nil {
		// Didn't find a function for this step. Either we didn't guess the name
		// properly, or the name is wrong. In the latter case, a warning will be
		// raised elsewhere.
		return nil, false
	}

	crds := make([]*apiextv1.CustomResourceDefinition, 0, len(pkg.Objs))
	for _, obj := range pkg.Objs {
		crd, ok := obj.(*apiextv1.CustomResourceDefinition)
		if !ok {
			continue
		}
		crds = append(crds, crd)
	}

	return crds, true
}

// possibleReposForFunction returns the possible repository paths for a function
// that the package manager would give a particular name. The package manager
// constructs these names by replacing `/` characters with `-` and stripping
// other non-DNS-friendly characters. This construction can't be reversed
// deterministically (since `my-org/cool-function` would get the same name as
// `my/org-cool-function`), hence why we have a set of possible candidates
// rather than just one name.
//
// Note that since embedded function repositories have a _ in their path, which
// gets stripped by the package manager, they'll never be matched properly here.
func possibleReposForFunction(name string) []string {
	sp := strings.Split(name, "-")
	possibles := make([]string, len(sp)-1)
	for i := 1; i < len(sp); i++ {
		acct := strings.Join(sp[:i], "-")
		repo := strings.Join(sp[i:], "-")
		possibles[i-1] = fmt.Sprintf("xpkg.upbound.io/%s/%s", acct, repo)
	}
	return possibles
}

func (c *CompositionValidator) marshal(data any) (*xpextv1.Composition, error) {
	u, ok := data.(*unstructured.Unstructured)
	if !ok {
		return nil, errors.New(errInvalidType)
	}

	b, err := u.MarshalJSON()
	if err != nil {
		return nil, err
	}

	var mcomp xpextv1.Composition
	err = json.Unmarshal(b, &mcomp)
	if err != nil {
		return nil, err
	}

	// convert v1.Composition to v1alpha1.CompositionRevision back to
	// v1.Composition to take advantage of default fields being set for various
	// sub objects within the v1.Composition definition.
	crev := icompositions.NewCompositionRevision(&mcomp, 1)
	comp := icomposite.AsComposition(crev)

	return comp, nil
}

type compositionValidator interface {
	validate(context.Context, int, resource.Composed) []error
}

// PatchesValidator validates the patches fields of a Composition.
type PatchesValidator struct {
	s *Snapshot
}

// NewPatchesValidator returns a new PatchesValidator.
func NewPatchesValidator(s *Snapshot) *PatchesValidator {
	return &PatchesValidator{
		s: s,
	}
}

// Validate validates that the composed resource is valid per the base
// resource's schema.
func (p *PatchesValidator) validate(ctx context.Context, idx int, cd resource.Composed) []error {
	cdgvk := cd.GetObjectKind().GroupVersionKind()
	v, ok := p.s.validators[cdgvk]
	if !ok {
		return gvkDNEWarning(cdgvk, fmt.Sprintf(resourceBaseFmt, idx, "apiVersion"))
	}

	result := v.Validate(ctx, cd)
	if result != nil {
		errs := []error{}
		for _, e := range result.Errors {
			var ve *verrors.Validation
			if !errors.As(e, &ve) {
				return []error{errors.New(errIncorrectErrType)}
			}
			ie := &validator.Validation{
				TypeCode: ve.Code(),
				Message:  fmt.Sprintf(errFmt, ve.Error(), cdgvk),
				Name:     fmt.Sprintf(resourceBaseFmt, idx, ve.Name),
			}
			errs = append(errs, ie)
		}
		return errs
	}

	return []error{fmt.Errorf(errInvalidValidationFmt, cdgvk)}
}
