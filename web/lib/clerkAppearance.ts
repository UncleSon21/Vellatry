// Clerk's sign-in and account screens in Vellatry's own tokens (app/globals.css), so
// signing in does not look like leaving the product. Plain values: it is passed from
// the server layout to Clerk's provider.
export const clerkAppearance = {
  variables: {
    colorPrimary: '#101828',
    colorPrimaryForeground: '#ffffff',
    colorForeground: '#101828',
    colorMutedForeground: '#475467',
    colorBackground: '#ffffff',
    colorInput: '#ffffff',
    colorInputForeground: '#101828',
    colorBorder: '#d0d5dd',
    colorDanger: '#b42318',
    borderRadius: '8px',
    fontFamily: '-apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif',
  },
  elements: {
    cardBox: { boxShadow: 'none', border: '1px solid #eaecf0' },
    footer: { background: '#f6f7f9' },
  },
}
