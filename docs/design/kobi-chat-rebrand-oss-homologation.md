# Homologación OSS — Kobi chat rebrand (`feat/kobi-chat-rebrand`)

**Qué es:** rebrand "Solo Kobi" — la superficie del chat (panel, toggle, Ask
buttons, sigil) se alinea con la identidad del sitio (Voltage Editorial: negro
cálido `#0a0b0a`, verde eléctrico `#00e07a` como acento nunca relleno, bisel
fino), con fixes de legibilidad de mensajes. **El tema global `--kb-*` de la
app NO cambia** (alcance aprobado explícitamente: solo Kobi).

Mockup as-built: `docs/design/kobi-chat-rebrand-ui-mockup.html`.

## Inventario para el port a OSS (`kubebolt`)

### 1. Copiar tal cual (byte-idénticos post-cambio — eran byte-idénticos antes)

| Archivo | Cambio |
|---|---|
| `apps/web/src/components/copilot/CopilotPanel.tsx` | Sweep `kb-*`→`kobi-*` + burbuja assistant card-v3 (`bg-kobi-card` + borde `kobi-border-accent`, `max-w-[min(65ch,100%)]`, 15px), burbuja user con tinte `kobi-accent-light` + borde, panel root `bg-kobi-bg`, header 15px/subtítulo 10px, chips del empty state a 13px con hover accent, footer 10.5px, send button `text-kobi-ink`, input bezel `bg-kobi-card`, Copy 10px |
| `apps/web/src/components/copilot/MarkdownRenderer.tsx` | Sweep tokens + cuerpo/listas/blockquote a 15px `leading-[1.65]`, h1 17px/h2 16px/h3 15px con tracking, código `bg-kobi-code-bg`/`text-kobi-code-text` (fuera el hardcode `#0d1117`/`#c9d1d9`), labels de code block a 10px; highlighter ligero por líneas para yaml/bash (claves yaml en accent, comentarios `#6b6864` fijo, cantidades con unidad en bold — colores fijos porque el código es oscuro en ambos temas); `th` en `text-kobi-text-secondary` sin uppercase (el header se diferencia siendo MÁS tenue + tinte del thead, que estaba silenciosamente roto: `bg-kobi-elevated/50` no se generaba sin triplet) |
| `apps/web/src/components/copilot/CopilotToggle.tsx` | Gradiente verde→violeta → `from-kobi-accent-fill to-kobi-accent-deep`; halo/sombra sobre `kobi-accent-fill`; sigil con `text-kobi-ink` (tinta oscura sobre verde; blanco-sobre-verde falla contraste); título del tooltip sólido `text-kobi-accent`; sweep tokens |
| `apps/web/src/components/copilot/AskCopilotButton.tsx` | Fuera los gradientes con `violet-*`; fondo `bg-kobi-accent-light`, label sólido `text-kobi-accent`, sombras a `rgba(0,224,122,…)` |
| `apps/web/src/components/copilot/ToolCallCard.tsx` | Sweep `kb-*`→`kobi-*` + `status-*`→`kobi-st-*` (iconos ok/error del header, cuerpo de error, check de Copy) |
| `apps/web/src/components/copilot/ActionProposalCard.tsx` | Sweep mecánico + `status-*`→`kobi-st-*` en TODAS las superficies (accentClasses, RiskBadge, blast radius, dry-run, progress); riesgo medio usa `border-kobi-st-warn-line` (el token lleva su propio alpha, no `/40`); riesgo bajo usa `bg-kobi-accent-dim` (~3%, del mockup) — con `accent-light` (10%) la tarjeta gemelaba con la burbuja del usuario |
| `apps/web/src/components/copilot/KobiMetricChartCard.tsx` | Sweep mecánico incl. `var(--kb-*)`→`var(--kobi-*)` en props de Recharts; `UtilizationChip` a `kobi-st-*` estilo outline del mbadge del mockup (las clases `kb-danger`/`kb-warning` NUNCA existieron en la paleta — el chip renderizaba sin color); línea/etiqueta de límite a `var(--kobi-st-error, #ef4444)` (rojo por tema) |
| `apps/web/src/components/copilot/ConversationList.tsx` | Sweep mecánico |
| `apps/web/src/components/kobi/KobiSigil.tsx` | `STATE_COLOR` completo a tokens de tema: `text-kobi-sigil-static/watching/investigating/awaiting` (antes `text-kobi-accent` + `emerald/amber/sky-400` fijos, ilegibles en light: 1.5–2.1:1) |
| `apps/web/src/components/copilot/CopilotPanel.tsx` (adicional) | Send button `bg-kobi-accent-fill`; banner de error, aviso de contexto ≥80% y MaxRounds a `kobi-st-*`; resize handles con indicador hairline de 3px (hit area sigue en 12px; la "L" de la esquina sigue el radio); panel flotante usa `.kobi-floating-shadow` (el docked mantiene `shadow-2xl` + `border-l`); colas estilo WhatsApp en ambas burbujas (rombo rotado 45° con borde en las caras salientes; burbuja user pasa a `bg-kobi-bubble-user` OPACO — tinte aplanado — porque una cola sobre tinte translúcido duplica el lavado; la cola de Kobi vive en el wrapper porque la burbuja tiene overflow-hidden) |

### 2. Cambios aditivos (aplicar como diff, NO copiar el archivo entero)

| Archivo | Cambio | Nota |
|---|---|---|
| `apps/web/tailwind.config.ts` | Bloque `kobi: { … }` añadido dentro de `theme.extend.colors` | Aditivo puro; el resto idéntico |
| `apps/web/src/styles/globals.css` | (a) Bloques `:root { --kobi-* }` y `.dark { --kobi-* }` añadidos tras los tokens `--kb-yaml-*` — incluyen light FRÍO alineado a `--kb-bg` (#f5f6fa, no el marfil del sitio), triplets RGB (incl. `--kobi-elevated-rgb` — sin él, `bg-kobi-elevated/50` y `/95` se descartaban en silencio), `--kobi-accent-fill(-rgb)`, `--kobi-accent-dim`, `--kobi-bubble-user` (tinte user aplanado a opaco), `--kobi-sigil-*` (400 dark / 700 light) y `--kobi-st-*` por tema (triplets + formas compuestas para SVG); (b) clase `.kobi-floating-shadow` + variante `.dark`; (c) familia IA recolorada a verde monocromo: keyframe `kb-ai-bezel-glow` (verde↔menta, fuera el violeta), conic-gradient del bezel (cresta `rgba(0,224,122,1)`→menta `rgba(198,255,227,.95)`), `:focus-within` (solo verdes), `kb-ai-shimmer-text` (verde→menta); (d) BORRADAS las vars muertas `--kb-kobi-watching/investigating/awaiting` (sin consumidores; su comentario "same in light and dark" era falso) | ⚠️ `globals.css` DIFIERE entre OSS y EE (EE añade estilos propios) — aplicar estos hunks como merge cuidadoso, no copia byte-a-byte |

### 3. Decisiones de diseño (racional)

- **Tokens con scope por consumo, no por wrapper**: las vars `--kobi-*` se
  definen en `:root`/`.dark` (variables nuevas = cero efecto en el resto de la
  app); solo las consumen las utilidades `kobi-*`. Tema claro = papel cálido
  con el verde profundo `#00b864` (contraste); código oscuro en AMBOS temas
  (convención existente).
- **Jerarquía de superficies en dark**: panel `#0a0b0a` (lo más profundo) →
  burbuja assistant `#131413` elevada con borde verde tenue → elevated
  `#1c1d1c`. Invierte el hundimiento anterior (burbuja más oscura que panel).
- **`--kobi-ink` `#06130c`**: tinta oscura sobre el acento (send button, sigil
  del toggle) — blanco sobre `#00e07a` no pasa contraste.
- **Tipografía**: sin fuente display nueva (costo de bundle); el wordmark usa
  `tracking-tight` + 15px. Si se quiere Space Grotesk, es una decisión aparte.

### 4. Verificación hecha en EE

- `npm run build` (tsc + vite): ✅
- `npm test` (vitest): ✅ 78/78
- Pendiente humano: QA visual claro/oscuro (panel docked ancho → confirmar
  medida 65ch; charts de KobiMetricChartCard con los tokens nuevos).

### 5. Deuda

- Homologar a OSS antes del siguiente release OSS (regla vigente).
- El hub de Autopilot (EE-only, `styles/autopilot.css`) sigue con la paleta
  vieja de la familia IA — alinear en un slice aparte si se desea.
