// Clerk's sign-in and account screens in Vellatry's own tokens (app/globals.css), so
// signing in does not look like leaving the product. Plain values: it is passed from
// the server layout to Clerk's provider.
export const clerkAppearance = {
  variables: {
    colorPrimary: '#15171c',
    colorPrimaryForeground: '#ffffff',
    colorForeground: '#15171c',
    colorMutedForeground: '#52555c',
    colorBackground: '#ffffff',
    colorInput: '#ffffff',
    colorInputForeground: '#15171c',
    colorBorder: '#d9d3c4',
    colorDanger: '#b42318',
    borderRadius: '8px',
    fontFamily: 'var(--font-sans), -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif',
  },
  elements: {
    cardBox: { boxShadow: 'none', border: '1px solid #e6e1d5' },
    footer: { background: '#f4f1ea' },
  },
}
