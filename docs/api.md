
The source guard resolves the data package's local type graph across files,
including aliases, nested exported fields and embeddings. Only reviewed scalar,
DTO and injected clock types may cross the boundary. `context.Context` is
permitted only at the input positions in approved operation signatures; exported
fields, aliases, nested containers and embeddings cannot carry it because `Value`
can expose arbitrary capabilities. The built-in `error` interface is likewise
restricted to approved operation results and error sentinels, rather than DTO
fields or aliases. Interfaces and callable capabilities fail closed. Operation
signatures (including receivers and caller arguments) are an explicit reviewed allowlist. Adding or changing an API requires
updating that contract with its permission/secrecy tests. Local types cannot
shadow the predeclared types used in the approved signatures. Private Store fields
remain implementation details. Parser/AST tooling imports are restricted to the
boundary checker package; application code cannot use parser.ParseFile as an
alternate database-file opener. The guard scans tests and build-tagged files for
imports too, and checks production API declarations regardless of build tags.
