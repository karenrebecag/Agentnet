# Seguridad

Corte 1 es la red local: join, send, read. Expose está apagado. `Open` devuelve error si `Enabled` no está puesto, y no busca ni descarga el binario del túnel.

Invitados se crean, listan y revocan solo en local. El token se devuelve una vez. El archivo guarda el hash. Aprobar un invitado por el túnel no existe.

Límites por invitado: 16 KiB, 10 por minuto, 100 por día, bloqueo de 15 minutos tras 5 fallos seguidos. Los contadores viven en memoria. Si el proceso reinicia sin `Restore`, el cupo del día vuelve a cero.

La auditoría guarda hora, invitado, acción, resultado y tamaño. No guarda el texto del mensaje ni rutas.

El lock es `flock` y cubre el append entero. En Windows ese lock no está. Este corte es Unix.

El escaneo de CI busca el prefijo de un home y direcciones de correo. No lleva una lista de identificadores reales.
