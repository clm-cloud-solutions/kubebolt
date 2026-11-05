import { describe, it, expect } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, Link } from 'react-router-dom'
import { NotFoundPage } from './NotFoundPage'

// An address no route matches used to render nothing at all. The page says so,
// shows the path that was tried (never the query string) and leads back.

function renderAt(entries: string[], index?: number) {
  return render(
    <MemoryRouter initialEntries={entries} initialIndex={index}>
      <Routes>
        <Route path="/home" element={<p>home page</p>} />
        <Route path="/from" element={<Link to="/nope/deeper">broken link</Link>} />
        <Route path="*" element={<NotFoundPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('NotFoundPage', () => {
  it('names the path that was tried, without the query string', () => {
    renderAt(['/nope/deeper?token=secret'])
    expect(screen.getByRole('heading', { name: "This page doesn't exist" })).toBeTruthy()
    expect(screen.getByText('/nope/deeper')).toBeTruthy()
    expect(screen.queryByText(/token=secret/)).toBeNull()
  })

  it('leads to Home, and offers no way back when the app opened on it', () => {
    renderAt(['/nope'])
    expect(screen.getByRole('link', { name: /Go to Home/ }).getAttribute('href')).toBe('/home')
    expect(screen.queryByRole('button', { name: 'Go back' })).toBeNull()
  })

  it('goes back to where the broken link was', () => {
    renderAt(['/from'])
    fireEvent.click(screen.getByText('broken link'))
    fireEvent.click(screen.getByRole('button', { name: 'Go back' }))
    expect(screen.getByText('broken link')).toBeTruthy()
  })
})
