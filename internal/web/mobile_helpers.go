package web

import (
	"fmt"
	"strings"
	"time"
)

var spanishDays = [...]string{
	"Domingo", "Lunes", "Martes", "Miércoles", "Jueves", "Viernes", "Sábado",
}

var spanishMonths = [...]string{
	"", "enero", "febrero", "marzo", "abril", "mayo", "junio",
	"julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre",
}

// formatDateSpanish formats a time.Time into Spanish "Viernes, 11 de septiembre".
func formatDateSpanish(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.In(economicLocation)
	weekday := spanishDays[t.Weekday()]
	day := t.Day()
	month := spanishMonths[t.Month()]
	return fmt.Sprintf("%s, %d de %s", weekday, day, month)
}

// formatDateHeadingSpanish produces friendly group headings like "Hoy, 11 de septiembre".
func formatDateHeadingSpanish(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	now := time.Now().In(economicLocation)
	tEcon := t.In(economicLocation)

	day := tEcon.Day()
	month := spanishMonths[tEcon.Month()]

	isToday := tEcon.Year() == now.Year() && tEcon.YearDay() == now.YearDay()
	isYesterday := tEcon.Year() == now.Year() && tEcon.YearDay() == now.YearDay()-1

	if isToday {
		return fmt.Sprintf("Hoy, %d de %s", day, month)
	}
	if isYesterday {
		return fmt.Sprintf("Ayer, %d de %s", day, month)
	}

	if tEcon.Year() == now.Year() {
		return fmt.Sprintf("%d de %s", day, month)
	}
	return fmt.Sprintf("%d de %s %d", day, month, tEcon.Year())
}

// inferCategoryIconAndEmoji picks appropriate UI icons and emojis based on category name.
func inferCategoryIconAndEmoji(name string) (icon string, emoji string) {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "aliment") || strings.Contains(lower, "super") || strings.Contains(lower, "comida") || strings.Contains(lower, "mercado") || strings.Contains(lower, "abarrote"):
		return "cart", "🛒"
	case strings.Contains(lower, "cafe") || strings.Contains(lower, "café") || strings.Contains(lower, "restauran") || strings.Contains(lower, "almuerzo") || strings.Contains(lower, "cena") || strings.Contains(lower, "bar"):
		return "coffee", "☕"
	case strings.Contains(lower, "transport") || strings.Contains(lower, "gasolin") || strings.Contains(lower, "combustible") || strings.Contains(lower, "auto") || strings.Contains(lower, "carro") || strings.Contains(lower, "uber") || strings.Contains(lower, "taxi"):
		return "car", "🚗"
	case strings.Contains(lower, "salud") || strings.Contains(lower, "farmacia") || strings.Contains(lower, "medicin") || strings.Contains(lower, "médic") || strings.Contains(lower, "medic") || strings.Contains(lower, "doctor") || strings.Contains(lower, "clinica") || strings.Contains(lower, "hospital"):
		return "pill", "💊"
	case strings.Contains(lower, "entreten") || strings.Contains(lower, "cine") || strings.Contains(lower, "streaming") || strings.Contains(lower, "netflix") || strings.Contains(lower, "spotify") || strings.Contains(lower, "juego") || strings.Contains(lower, "ocio"):
		return "entertainment", "🎬"
	case strings.Contains(lower, "nómina") || strings.Contains(lower, "nomina") || strings.Contains(lower, "sueldo") || strings.Contains(lower, "salario") || strings.Contains(lower, "ingreso") || strings.Contains(lower, "trabajo") || strings.Contains(lower, "honorario") || strings.Contains(lower, "profesional") || strings.Contains(lower, "freelance") || strings.Contains(lower, "inversi") || strings.Contains(lower, "rendimiento"):
		return "briefcase", "💼"
	default:
		return "generic", "🏷️"
	}
}

// accountTypeFriendlyLabel provides Spanish label for account container and subtype.
func accountTypeFriendlyLabel(acct Account) string {
	switch acct.Type {
	case AccountTypeBank:
		if acct.Subtype != nil {
			if *acct.Subtype == AccountSubtypeChecking {
				return "Cuenta Corriente"
			}
			if *acct.Subtype == AccountSubtypeSavings {
				return "Cuenta de Ahorro"
			}
		}
		return "Cuenta Bancaria"
	case AccountTypeCash:
		return "Efectivo"
	case AccountTypeWallet:
		return "Billetera Digital"
	default:
		return "Cuenta"
	}
}
