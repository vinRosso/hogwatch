// Package deploycheck holds tests over the shipped deployment files
// (compose.yaml, .env.example, deploy/Dockerfile). They keep the security
// invariants about container privileges true as the files change.
package deploycheck
