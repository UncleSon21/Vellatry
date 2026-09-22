// The Vellatry mark: a ring with one segment highlighted, the blindspot. It takes the
// text colour for the ring and the highlighter for the segment.
export function Mark({ className, size = 22 }: { className?: string; size?: number }) {
  return (
    <svg className={className} width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
      <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="3" />
      <path d="M12 3 A9 9 0 0 1 21 12" fill="none" stroke="var(--lime)" strokeWidth="3" strokeLinecap="round" />
    </svg>
  )
}
