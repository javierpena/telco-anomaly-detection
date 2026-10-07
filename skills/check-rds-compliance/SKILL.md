---
name: check-rds-compliance
description: Validate a named managed cluster against the Telco RAN Reference Design Specification using kube-compare MCP.
---

# Check RAN RDS Compliance

## Verify

Take the managed (spoke) cluster name from the request; if absent, obtain it before validating. The MCP server runs on the ACM hub: call `kube_compare_validate_rds` with `managed_cluster` set to that **spoke** name, not the hub. Do not change cluster resources.

## Report

Report every `ValidationIssue` returned, with its requirement level and evidence. For `required` deviations, propose a specific remediation without applying it. If the tool fails, returns incomplete results, or the cluster cannot be identified, report **unable to verify** and the reason; do not describe an incomplete run as compliant. Only report compliance when validation completes and no issues are returned.
