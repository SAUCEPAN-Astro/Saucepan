# Third-party notices

Saucepan is released under the Apache License 2.0 (see [`LICENSE`](LICENSE)). It
depends on the third-party packages below. This file records the ones whose
licenses are not themselves permissive (MIT / BSD / Apache-2.0); everything
else in the dependency set is MIT, BSD-2/3-Clause, or Apache-2.0 and imposes
no notice obligation.

A full dependency-license audit (2026-09-01) found **no strong-copyleft
(GPL / AGPL / SSPL) dependencies** in any per-service manifest. The two entries
below are *file-level* ("weak") copyleft and are the field-standard libraries
for their job; they are used unmodified, as installed from their package
registries.

| Package | Ecosystem | License | Used by | Role |
|---|---|---|---|---|
| `github.com/eclipse/paho.mqtt.golang` | Go | EPL-2.0 / EDL-1.0 (dual) | `SaucepanServer/task-server`, `tools/devkit/emulator` | MQTT client |
| `psycopg2-binary` | Python | LGPL-3.0-with-OpenSSL-exception | `SaucepanServer/storage-server/datalake` | PostgreSQL driver |

Weak-copyleft here means: if you modify *those libraries' own source files* and
distribute the result, those files must remain under their original license.
Using them as dependencies — which is all this project does — carries no
copyleft effect on Saucepan's own Apache-2.0-licensed code.

Full audit detail is kept with the maintainer; regenerate with any standard
license scanner (`go-licenses`, `pip-licenses`) against the per-service
manifests listed in [`requirements.txt`](requirements.txt).
