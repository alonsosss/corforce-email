package domain

// AddressBookEntry es un companero de empresa en la libreta de direcciones compartida: solo
// su direccion y su nombre visible.
type AddressBookEntry struct {
	Address     string
	DisplayName string
}
