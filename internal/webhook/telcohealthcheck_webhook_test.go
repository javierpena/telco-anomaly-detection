package webhook_test

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/webhook"
)

func TestValidateCreate_CanonicalName(t *testing.T) {
	v := &webhook.TelcoHealthcheckValidator{}
	thc := &ranv1alpha1.TelcoHealthcheck{
		ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName},
	}
	warnings, err := v.ValidateCreate(context.Background(), thc)
	if err != nil {
		t.Errorf("expected nil error for canonical name, got: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
}

func TestValidateCreate_NonCanonicalName(t *testing.T) {
	v := &webhook.TelcoHealthcheckValidator{}
	thc := &ranv1alpha1.TelcoHealthcheck{
		ObjectMeta: metav1.ObjectMeta{Name: "wrong-name"},
	}
	_, err := v.ValidateCreate(context.Background(), thc)
	if err == nil {
		t.Error("expected error for non-canonical name, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), ranv1alpha1.TelcoHealthcheckCanonicalName) {
		t.Errorf("expected error to mention canonical name %q, got: %v", ranv1alpha1.TelcoHealthcheckCanonicalName, err)
	}
}

func TestValidateUpdate_AlwaysAllowed(t *testing.T) {
	v := &webhook.TelcoHealthcheckValidator{}
	thc := &ranv1alpha1.TelcoHealthcheck{
		ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName},
	}
	warnings, err := v.ValidateUpdate(context.Background(), thc, thc)
	if err != nil {
		t.Errorf("expected nil error for update, got: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
}

func TestValidateDelete_AlwaysAllowed(t *testing.T) {
	v := &webhook.TelcoHealthcheckValidator{}
	thc := &ranv1alpha1.TelcoHealthcheck{
		ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName},
	}
	warnings, err := v.ValidateDelete(context.Background(), thc)
	if err != nil {
		t.Errorf("expected nil error for delete, got: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
}
