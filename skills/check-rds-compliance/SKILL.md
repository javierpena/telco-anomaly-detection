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
- The MCP server is running remotely on an ACM hub cluster. Make sure you pass the `managed_cluster` parameter to the tool calls supporting it, to reference the cluster being checked

## Step 1: Verify RDS compliance

1. Get the cluster name to be checked for RDS compliance.
2. Use the `kube_compare_validate_rds` tool to verify RDS compliance against the RAN specification. Make sure the `managed_cluster` parameter is included.

## Step 2: Analyze data and generate report

Report on all deviations found, displayed as `ValidationIssues` in the tool output. For all deviations marked as `required`, propose a fix to remediate them.

