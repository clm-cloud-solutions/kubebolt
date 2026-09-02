import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { parseClusterDisplayName } from '@/utils/cluster'

/**
 * useClusterLabel — de identificador de cluster a nombre legible.
 *
 * Las vistas de consumo subieron a altura de organización, así que cada fila
 * tiene que decir DÓNDE se gastó; y lo que traen guardado no es un nombre, es
 * un identificador crudo. Peor: no es el mismo en las dos caras. Kobi guarda el
 * NOMBRE DE CONTEXTO de cuando ocurrió la sesión (`agent:<uid>`), y Autopilot
 * guarda el UID a secas, porque su almacén se indexa por el UID que escribe su
 * poller. Un resolutor por vista habría dejado una de las dos mostrando
 * jeroglíficos, así que éste acepta las dos formas.
 *
 * Devuelve cadena vacía cuando no resuelve —cluster dado de baja, contexto
 * renombrado— y nunca el identificador crudo: un `agent:6032e959-…` en una
 * columna no es información, es ruido que además parece un error. Que la vista
 * decida cómo pintar el hueco.
 *
 * Usa la MISMA queryKey que el resto de la app, así que no añade una petición:
 * la lista de clusters ya está cacheada por la barra superior.
 */
export function useClusterLabel(): (idOrContext: string) => string {
  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    staleTime: 5 * 60_000,
  })
  // Respaldo para los clusters que YA NO están dados de alta. La lista de arriba
  // es la de los vivos —es la que alimenta el selector—, pero estas vistas son
  // históricas: enseñan gasto de clusters que se dieron de baja después. Sin
  // esto, esas filas salían con un guion, o sea «alguien gastó 1.034 créditos en
  // algún sitio», que se lee como un fallo de la aplicación y no como un cluster
  // retirado. El nombre existe porque el registro es durable a propósito.
  const { data: durableNames } = useQuery({
    queryKey: ['cluster-names'],
    queryFn: api.getClusterNames,
    staleTime: 5 * 60_000,
    retry: false, // en OSS puede no existir; es una etiqueta, no un dato crítico
  })
  return (idOrContext: string) => {
    if (!idOrContext) return ''
    const list = clusters ?? []
    // El UID primero: es la identidad estable. El contexto puede renombrarse y
    // hasta reutilizarse para otro cluster, así que casar por él antes podría
    // atribuir el gasto al cluster equivocado.
    const hit =
      list.find((c) => c.clusterId && c.clusterId === idOrContext) ??
      list.find((c) => c.context === idOrContext)
    if (hit) return parseClusterDisplayName(hit)
    // El mapa durable viene indexado por las DOS identidades, así que sirve
    // tanto al UID de Autopilot como al nombre de contexto de Kobi.
    return durableNames?.[idOrContext] ?? ''
  }
}
