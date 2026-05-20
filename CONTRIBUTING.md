# Contributing to GitLab Process History Exporter

Thank you for choosing to contribute to our project! We welcome code contributions, issue reports, and documentation improvements.

## Pull Request Checklist
Before submitting a pull request, please verify that your changes meet these standards:
1. **Formatting**: Ensure your changes are formatted using `make fmt`.
2. **Quality Rules**: Run `make lint` and fix any static analysis issues raised by `golangci-lint`.
3. **Automated Tests**: Execute `make test` and verify that all test suites pass.
4. **Coverage**: New code should have corresponding unit tests. Aim to keep code coverage above 80%.

## Development Setup
1. Install Go 1.21+ (1.22+ is recommended).
2. Clone the repository and compile the exporter locally:
   ```bash
   make build
   ```
3. Run tests locally:
   ```bash
   make test
   ```

## Commit Message Guidelines
We prefer clear, descriptive commit messages. When possible, follow the [Conventional Commits](https://www.conventionalcommits.org/) format:
* `feat: add environment variable search filter`
* `fix: prevent potential race condition during scrapers teardown`
* `docs: update setup instructions in README.md`
