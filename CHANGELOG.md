# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial SDK with Config sub-client (CRUD operations).
- Functional options for client configuration (`WithBaseURL`, `WithTimeout`, `WithHTTPClient`).
- Typed error hierarchy with `errors.Is()`/`errors.As()` support.
- Bearer token authentication.
- JSON:API request/response handling.
- CI workflow for Go 1.21, 1.22, 1.23.
