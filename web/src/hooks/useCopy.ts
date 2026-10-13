import { useState } from 'react'

/**
 * Copies text to the clipboard; `copied` stays true for 2s afterwards so the
 * button can confirm it.
 */
export function useCopy(): [boolean, (text: string) => void] {
  const [copied, setCopied] = useState(false)
  const copy = (text: string) => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }
  return [copied, copy]
}
