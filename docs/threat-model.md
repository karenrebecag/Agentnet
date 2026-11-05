# Modelo de amenazas

Estado: borrador. `agentnet expose` solo arranca cuando la persona lo ejecuta.

## Activos

Los agentes locales, sus herramientas y los archivos de la máquina. El token de un invitado. El texto que un tercero mandaría por un túnel.

## Qué no puede pasar

Un mensaje de fuera no es un turno de la persona dueña de la máquina. No abre herramientas ni archivos. `list` y `get_reply` no devuelven rutas, directorio de trabajo ni transcript.

La URL de un túnel no es el secreto. Hace falta un token por invitado, revocable. `get_reply` va atado a ese token y el id no se puede adivinar.

El apagado a plazo y el kill switch tienen que matar el proceso del túnel, no solo dejar de aceptar conexiones.

El host de la petición tiene que ser el de esa sesión. Cualquier otro se rechaza.

## Qué hace expose

`agentnet expose` busca `cloudflared` en el PATH y no lo descarga. Si no está, sale con error. Hay un túnel por sesión. Si el proceso muere, la sesión termina y no se reinicia. El plazo y `agentnet expose stop` matan ese proceso.

Cinco autenticaciones fallidas desde la misma dirección la bloquean 15 minutos. Cada 20 fallos se avisa al host. Esos fallos no apagan la sesión: si la apagaran, cualquiera podría tumbarla. La auth es Bearer. El archivo guarda el hash del token, no el token.

## Abierto

Los cupos no sobreviven un reinicio si nadie llama `Restore`. Windows no tiene este lock.

El release por tag arma binarios, checksums y SBOM, y firma la procedencia con attestation de GitHub. No hay llave GPG en el repo. No publica fórmula de Homebrew: el tap no existe y el token del workflow no puede escribir otro repositorio.
