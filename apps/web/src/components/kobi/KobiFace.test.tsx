import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { KobiFace } from './KobiFace'

// The eyes say the state with their silhouette: a fixed capsule cut by two
// lids in the visor's color, which kobi-face.css places per state. What the
// markup has to guarantee is that every face has its own pieces and its own
// clip, or a lid would show on the shell (or clip by another face's visor).

describe('KobiFace eyes', () => {
  it('draws each eye as a capsule and two lids, clipped by that face’s own visor', () => {
    const { container } = render(
      <>
        <KobiFace size={64} state="investigating" />
        <KobiFace size={24} state="verifying" />
      </>,
    )
    const faces = [...container.querySelectorAll('svg.kf')]
    expect(faces).toHaveLength(2)

    const clipIds = faces.map((svg) => {
      const clip = svg.querySelector('clipPath')
      expect(clip).not.toBeNull()
      const clipped = svg.querySelector(`[clip-path="url(#${clip!.id})"]`)
      expect(clipped?.querySelector('.kf-eyes')).not.toBeNull()
      const eyes = clipped!.querySelectorAll('.kf-eye')
      expect(eyes).toHaveLength(2)
      eyes.forEach((eye) => {
        expect(eye.querySelectorAll('.kf-ball')).toHaveLength(1)
        expect(eye.querySelectorAll('.kf-lid-up')).toHaveLength(1)
        expect(eye.querySelectorAll('.kf-lid-lo')).toHaveLength(1)
      })
      return clip!.id
    })
    expect(new Set(clipIds).size).toBe(2)
  })

  it('moves the lids in fractions of the eye box of its size', () => {
    const { container } = render(
      <>
        <KobiFace size={64} />
        <KobiFace size={24} />
      </>,
    )
    const [l, s] = [...container.querySelectorAll<SVGSVGElement>('svg.kf')]
    expect(l.style.getPropertyValue('--kf-h')).toBe('14px')
    expect(s.style.getPropertyValue('--kf-h')).toBe('16px')
  })

  it('keeps the gloss over the eyes, so the upper lids never hide it', () => {
    const { container } = render(<KobiFace size={128} />)
    const eyes = container.querySelector('.kf-eyes')!
    const gloss = container.querySelector('.kf-gloss')!
    expect(eyes.compareDocumentPosition(gloss) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
