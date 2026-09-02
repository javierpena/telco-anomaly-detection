---
name: check-rds-compliance
description: Run a compliance check against the Telco RAN Reference Design Specification for this cluster. Use when user wants to verify if the cluster configuration complies with the requirements of the Telco RAN RDS.
---

# Check RAN RDS Compliance

## When to Use

- Use this skill when you need to verify the cluster configuration against the RAN RDS
- This skill is helpful to identify any deviations from the Reference Design Specification

## Rules

- Use available MCP tools whenever possible
- The MCP server is running remotely. Make sure a valid kubeconfig file for the current cluster is passed as the `kubeconfig` parameter on any tool call

## Step 1: Verify RDS compliance

1. Create a valid kubeconfig file for the cluster. You can get if from the `node-kubeconfigs` Secret in the `openshift-kube-apiserver` namespace. Use the data from `lb-ext.kubeconfig` for the kubeconfig.
2. Use the `kube_compare_validate_rds` tool to verify RDS compliance against the RAN specification. Make sure the `kubeconfig` and `context` parameters are included, and refer to the kubeconfig file retrieved in the previous item.  Make sure the `kubeconfig` parameter is provided either via raw kubeconfig YAML content or base64-encoded kubeconfig

## Step 2: Analyze data and generate report

Report on all deviations found, displayed as `ValidationIssues` in the tool output. For all deviations marked as `required`, propose a fix to remediate them.

