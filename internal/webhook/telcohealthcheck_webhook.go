package webhook

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

// TelcoHealthcheckValidator implements admission.CustomValidator for TelcoHealthcheck.
// It enforces the singleton invariant by rejecting CREATE requests for any name
// other than TelcoHealthcheckCanonicalName.
type TelcoHealthcheckValidator struct{}

var _ admission.CustomValidator = &TelcoHealthcheckValidator{}

// SetupWebhookWithManager registers the validating webhook with the manager.
// +kubebuilder:webhook:path=/validate-ran-openshift-io-v1alpha1-telcohealthcheck,mutating=false,failurePolicy=fail,sideEffects=None,groups=ran.openshift.io,resources=telcohealthchecks,verbs=create,versions=v1alpha1,name=vtelcohealthcheck.kb.io,admissionReviewVersions=v1
func (v *TelcoHealthcheckValidator) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&ranv1alpha1.TelcoHealthcheck{}).
		WithValidator(v).
		Complete()
}

func (v *TelcoHealthcheckValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	thc, ok := obj.(*ranv1alpha1.TelcoHealthcheck)
	if !ok {
		return nil, fmt.Errorf("expected TelcoHealthcheck, got %T", obj)
	}
	if thc.Name != ranv1alpha1.TelcoHealthcheckCanonicalName {
		return nil, fmt.Errorf("TelcoHealthcheck name must be %q; got %q",
			ranv1alpha1.TelcoHealthcheckCanonicalName, thc.Name)
	}
	return nil, nil
}

func (v *TelcoHealthcheckValidator) ValidateUpdate(_ context.Context, _, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *TelcoHealthcheckValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
