package v1alpha1

import (
	"context"
	"os"
	"testing"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// Exercise the generated CRD rules with Kubernetes' CEL validator, including
// cases where a single override makes an inherited window invalid.
func TestJitterAdmissionValidation(t *testing.T) {
	f, err := os.Open("../../config/crd/bases/ran.openshift.io_telcohealthchecks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.NewYAMLOrJSONDecoder(f, 4096).Decode(&crd); err != nil {
		t.Fatal(err)
	}
	props := &apiextensionsinternal.JSONSchemaProps{}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
		crd.Spec.Versions[0].Schema.OpenAPIV3Schema, props, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := schema.NewStructural(props)
	if err != nil {
		t.Fatal(err)
	}
	validator := cel.NewValidator(structural, true, 1000000)
	if validator == nil {
		t.Fatal("expected CEL validator for generated CRD")
	}
	for _, tc := range []struct {
		name   string
		global map[string]interface{}
		rds    map[string]interface{}
		bad    bool
	}{
		{name: "defaults"},
		{name: "global zero", global: map[string]interface{}{"minJitter": "0s", "maxJitter": "0s"}},
		{name: "global fixed positive", global: map[string]interface{}{"minJitter": "1s", "maxJitter": "1s"}},
		{name: "global single bound invalid", global: map[string]interface{}{"maxJitter": "0s"}, bad: true},
		{name: "global negative", global: map[string]interface{}{"minJitter": "-1s"}, bad: true},
		{name: "check inherits global", global: map[string]interface{}{"maxJitter": "1m"},
			rds: map[string]interface{}{"minJitter": "45s"}},
		{name: "check single bound invalid", rds: map[string]interface{}{"maxJitter": "0s"}, bad: true},
		{name: "check negative", rds: map[string]interface{}{"minJitter": "-1s"}, bad: true},
		{name: "check fixed positive", rds: map[string]interface{}{"minJitter": "10s", "maxJitter": "10s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			periodic := map[string]interface{}{"period": "1h"}
			for k, v := range tc.global {
				periodic[k] = v
			}
			if tc.rds != nil {
				rds := map[string]interface{}{"enabled": true}
				for k, v := range tc.rds {
					rds[k] = v
				}
				periodic["rdsCompliance"] = rds
			}
			obj := map[string]interface{}{"spec": map[string]interface{}{"periodicHealthChecks": periodic}}
			errs, _ := validator.Validate(context.Background(), nil, structural, obj, nil, 1000000)
			if (len(errs) != 0) != tc.bad {
				t.Fatalf("admission errors: %v; expected invalid: %t", errs, tc.bad)
			}
		})
	}
}
