//go:build !ee

package main

// buildEdition labels kubebolt_build_info: "oss" here, "ee" in an EE build
// (edition_ee.go); a multi-tenant EE build reports "saas".
const buildEdition = "oss"
