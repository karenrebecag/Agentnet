# Modelo de amenazas

Estado: borrador. No habilita expose. `Enabled` sigue en falso hasta que este documento se acepte.

## Activos

Los agentes locales, sus herramientas y los archivos de la máquina. El token de un invitado. El texto que un tercero mandaría por un túnel.

## Qué no puede pasar

Un mensaje de fuera no es un turno de la persona dueña de la máquina. No abre herramientas ni archivos. `list` y `get_reply` no devuelven rutas, directorio de trabajo ni transcript.

La URL de un túnel no es el secreto. Hace falta un token por invitado, revocable. `get_reply` va atado a ese token y el id no se puede adivinar.

El apagado a plazo y el kill switch tienen que matar el proceso del túnel, no solo dejar de aceptar conexiones.

El host de la petición tiene que ser el de esa sesión. Cualquier otro se rechaza.

## Qué hace este scaffold

El túnel no arranca. Si alguien pone `Enabled` y el binario no está, sale con error y no lo instala. El supervisor mata el grupo de procesos al cancelar, al cumplir el plazo, o tras 20 fallos.

## Abierto

Bearer o OAuth queda sin cerrar. Los cupos no sobreviven un reinicio si nadie llama `Restore`. Windows no tiene este lock.

El release por tag arma binarios, checksums y SBOM, y firma la procedencia con attestation de GitHub. No hay llave GPG en el repo. No publica fórmula de Homebrew: el tap no existe y el token del workflow no puede escribir otro repositorio.

Los jobs de Actions de esta cuenta no arrancan mientras el límite de gasto esté bloqueado. El workflow escrito no es evidencia de que corrió.
