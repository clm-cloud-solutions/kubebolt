import { Cloud, Server } from 'lucide-react'

// El icono del proveedor cloud de un cluster.
//
// Marcas dibujadas a mano en SVG en vez de importar un paquete de logotipos:
// son cuatro formas simples, y una dependencia nueva para esto arrastraría un
// bundle entero de iconos que nadie más usa. Cada una es una silueta
// RECONOCIBLE, no el logotipo oficial — no se reproducen marcas registradas con
// sus colores y proporciones exactas dentro del producto.
//
// El proveedor lo determina el backend a partir del `providerID` del nodo
// (cluster/profile.go), no de una etiqueta que el operador pueda escribir mal.
// Cuando no se ha podido determinar, este componente devuelve `null` y quien lo
// llama simplemente no pinta nada: un cluster on-prem marcado como AWS es peor
// que uno sin marcar.

export type CloudProvider = 'aws' | 'gcp' | 'azure' | 'kind' | 'onprem' | string

/** Nombre legible, para el `title` y para el texto junto al icono. */
export const PROVIDER_LABEL: Record<string, string> = {
  aws: 'AWS',
  gcp: 'Google Cloud',
  azure: 'Azure',
  kind: 'kind (local)',
  k3s: 'k3s',
  minikube: 'minikube',
  docker: 'Docker Desktop',
  onprem: 'On-prem',
}

export function providerLabel(provider?: string): string {
  if (!provider) return ''
  return PROVIDER_LABEL[provider.toLowerCase()] ?? provider
}

export function CloudProviderIcon({
  provider,
  className = 'w-3.5 h-3.5',
}: {
  provider?: string
  className?: string
}) {
  const p = (provider ?? '').toLowerCase()
  if (!p) return null

  const common = { className, viewBox: '0 0 24 24', fill: 'currentColor', 'aria-hidden': true } as const

  if (p === 'aws') {
    // La sonrisa de AWS: el arco con su punta de flecha.
    return (
      <svg {...common}>
        <path d="M6.8 10.4c0 .3 0 .6.1.8l.4.9v.2c0 .1-.1.2-.2.3l-.5.3h-.2c-.1 0-.2 0-.3-.2l-.4-.4-.3-.5c-.6.7-1.3 1-2.2 1-.6 0-1.1-.2-1.5-.6s-.6-.9-.6-1.5c0-.7.2-1.2.7-1.6s1.1-.6 2-.6c.3 0 .6 0 .9.1l1 .2v-.6c0-.6-.1-1-.4-1.2s-.7-.3-1.3-.3l-.9.1-.9.3h-.3c-.2 0-.2-.1-.2-.3v-.4c0-.1 0-.2.1-.3l.3-.2 1-.3 1.2-.1c.9 0 1.6.2 2 .6s.6 1 .6 1.9v2.4zm-3 1.1.8-.1.9-.5.2-.5v-.8l-.8-.2h-.8c-.6 0-1 .1-1.2.3s-.4.5-.4.8.1.6.3.8.4.2.8.2zm6 .8-.4-.1-.2-.3-1.7-5.6v-.4c0-.2 0-.3.2-.3h.8l.3.1.2.3 1.2 4.8L11.5 6l.2-.3.3-.1h.7l.3.1.2.3 1.2 4.9 1.3-4.9.2-.3.3-.1h.8c.2 0 .3.1.3.3v.2l-.1.2-1.8 5.6-.2.3-.3.1h-.7l-.4-.1-.2-.3-1.1-4.7-1.1 4.7-.2.3-.4.1h-.7zm9.6.3c-.4 0-.9 0-1.3-.1l-.9-.3-.3-.3-.1-.3v-.4c0-.2.1-.3.2-.3h.2l.3.1.8.3.9.1c.5 0 .8-.1 1.1-.2s.4-.4.4-.6-.1-.4-.2-.5l-.8-.4-1.1-.4c-.6-.2-1-.4-1.2-.8s-.4-.7-.4-1.1c0-.3.1-.6.2-.8l.5-.6.7-.4 1-.1h.6l.5.1.5.2.3.1.3.2v.6c0 .2-.1.3-.2.3l-.4-.1c-.4-.2-.9-.3-1.4-.3-.4 0-.8.1-1 .2s-.3.3-.3.6.1.4.3.5l.9.4 1.1.4c.5.2.9.4 1.1.7s.3.7.3 1.1c0 .3-.1.6-.2.9s-.3.5-.6.7l-.8.4-1.1.1z" />
        <path d="M21.5 17.6c-2.4 1.8-5.8 2.7-8.8 2.7-4.2 0-8-1.5-10.8-4.1-.2-.2 0-.5.3-.3 3.1 1.8 6.8 2.8 10.7 2.8 2.6 0 5.5-.5 8.2-1.7.4-.2.7.3.4.6zm1-1.1c-.3-.4-2-.2-2.8-.1-.2 0-.3-.2-.1-.3 1.4-1 3.6-.7 3.9-.4s-.1 2.5-1.4 3.5c-.2.2-.4.1-.3-.1.3-.8.9-2.3.7-2.6z" />
      </svg>
    )
  }

  if (p === 'gcp' || p === 'gke' || p === 'google') {
    // El hexágono partido de Google Cloud, simplificado a cuatro cuñas.
    return (
      <svg {...common}>
        <path d="M12 2.2 3.5 7.1v9.8L12 21.8l8.5-4.9V7.1L12 2.2zm0 2.3 6.5 3.8-3.2 1.9L12 8.3 8.7 10.2 5.5 8.3 12 4.5zM5.5 10.6l3.2 1.9v3.7l-3.2-1.9v-3.7zm13 0v3.7l-3.2 1.9v-3.7l3.2-1.9zm-8.2 4.8L12 14.3l1.7 1.1v2.2L12 18.7l-1.7-1.1v-2.2z" />
      </svg>
    )
  }

  if (p === 'azure' || p === 'aks' || p === 'microsoft') {
    // La "A" angular de Azure.
    return (
      <svg {...common}>
        <path d="M9.1 3h5.4l-5.6 16.6a.9.9 0 0 1-.8.6H3.9a.9.9 0 0 1-.8-1.2L8.3 3.6a.9.9 0 0 1 .8-.6z" />
        <path d="M16.6 15.1H8.1a.4.4 0 0 0-.3.7l5.5 5.1a.9.9 0 0 0 .6.2h4.9l-2.2-6z" opacity=".75" />
        <path d="M15.4 8.4h-4.6l4.6 12.7h5.4a.9.9 0 0 0 .8-1.2l-5.4-11a.9.9 0 0 0-.8-.5z" opacity=".55" />
      </svg>
    )
  }

  if (p === 'kind' || p === 'k3s' || p === 'minikube' || p === 'docker' || p === 'local') {
    // Local: un servidor, no una nube. La distinción importa — un cluster de
    // desarrollo en el portátil no es infraestructura que nadie factura.
    return <Server className={className} aria-hidden />
  }

  // Proveedor conocido pero sin marca propia (on-prem, OpenStack, un cloud
  // regional): una nube genérica dice «corre en algún sitio» sin mentir sobre
  // cuál.
  return <Cloud className={className} aria-hidden />
}
