package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

type CalendarService struct {
	srv       *calendar.Service
	calID     string
	StartHour int
	EndHour   int
	WorkDays  []int // 0=Domingo, 1=Lunes...
}

// Estructura para mapear el JSON
type TenantCalendarConfig struct {
	CalendarID string `json:"calendar_id"`
	StartHour  int    `json:"start_hour"`
	EndHour    int    `json:"end_hour"`
	WorkDays   []int  `json:"work_days"`
}

func NewCalendarService(tenant string) (*CalendarService, error) {
	ctx := context.Background()
	credsFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credsFile == "" {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS no está en .env")
	}

	configRoot := "configs"
	configPath := filepath.Join(configRoot, tenant, "calendar.json")

	// Valores por defecto (si faltan en el JSON)
	cfg := TenantCalendarConfig{
		StartHour: 9,
		EndHour:   17,
		WorkDays:  []int{1, 2, 3, 4, 5}, // Lun-Vie
	}

	// Cargamos config si existe
	if _, err := os.Stat(configPath); err == nil {
		b, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("error leyendo config calendario: %w", err)
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("json calendario inválido: %w", err)
		}
	} else {
		// Fallback por env vars si no hay JSON (retrocompatibilidad)
		cfg.CalendarID = os.Getenv("GOOGLE_CALENDAR_ID")
	}

	if cfg.CalendarID == "" {
		return nil, fmt.Errorf("no se encontró calendar_id para el tenant %s", tenant)
	}

	// Validaciones básicas para que no explote el loop
	if cfg.StartHour < 0 {
		cfg.StartHour = 9
	}
	if cfg.EndHour > 24 {
		cfg.EndHour = 17
	}
	if len(cfg.WorkDays) == 0 {
		cfg.WorkDays = []int{1, 2, 3, 4, 5}
	}

	srv, err := calendar.NewService(ctx, option.WithCredentialsFile(credsFile))
	if err != nil {
		return nil, fmt.Errorf("error creando cliente calendar: %v", err)
	}

	return &CalendarService{
		srv:       srv,
		calID:     cfg.CalendarID,
		StartHour: cfg.StartHour,
		EndHour:   cfg.EndHour,
		WorkDays:  cfg.WorkDays,
	}, nil
}

type Slot struct {
	ID       string
	Text     string
	ISOValue string
}

func (c *CalendarService) GetNextAvailableSlots() ([]Slot, error) {
	// 1. Cargamos la zona horaria
	loc, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		fmt.Printf("⚠️ No se pudo cargar zona horaria, usando Local: %v\n", err)
		loc = time.Local
	}

	now := time.Now().In(loc)

	// Buscamos ventanas en los próximos 7 días (por si viene finde largo)
	minTime := now.Format(time.RFC3339)
	maxTime := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)

	query := &calendar.FreeBusyRequest{
		TimeMin: minTime,
		TimeMax: maxTime,
		Items:   []*calendar.FreeBusyRequestItem{{Id: c.calID}},
	}

	res, err := c.srv.Freebusy.Query(query).Do()
	if err != nil {
		return nil, err
	}

	busyRanges := res.Calendars[c.calID].Busy
	var slots []Slot
	counter := 1

	// Iteramos los próximos días hasta encontrar 3 slots
	for d := 0; d < 10; d++ { // Buscamos hasta 10 días adelante
		if len(slots) >= 3 {
			break
		}

		day := now.AddDate(0, 0, d)
		weekday := int(day.Weekday()) // 0=Domingo, 1=Lunes...

		// Chequeamos si hoy se trabaja
		isWorkingDay := false
		for _, wd := range c.WorkDays {
			if wd == weekday {
				isWorkingDay = true
				break
			}
		}
		if !isWorkingDay {
			continue
		}

		// Iteramos las horas configuradas
		for h := c.StartHour; h < c.EndHour; h++ {
			if len(slots) >= 3 {
				break
			}

			slotStart := time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, loc)
			slotEnd := slotStart.Add(1 * time.Hour)

			// No mostrar horas pasadas
			if slotStart.Before(now) {
				continue
			}

			// Chequeo de ocupación en Google
			isBusy := false
			for _, busy := range busyRanges {
				bStart, _ := time.Parse(time.RFC3339, busy.Start)
				bEnd, _ := time.Parse(time.RFC3339, busy.End)

				// Intersección de horarios
				if slotStart.Before(bEnd) && slotEnd.After(bStart) {
					isBusy = true
					break
				}
			}

			if !isBusy {
				slots = append(slots, Slot{
					ID:       fmt.Sprintf("SLOT_%d", counter),
					Text:     fmt.Sprintf("%s %s", slotStart.Format("Mon 02"), slotStart.Format("15:04")),
					ISOValue: slotStart.Format(time.RFC3339),
				})
				counter++
			}
		}
	}

	return slots, nil
}

func (c *CalendarService) CreateAppointment(isoStart, contactName, contactPhone string) error {
	startTime, err := time.Parse(time.RFC3339, isoStart)
	if err != nil {
		return fmt.Errorf("fecha inválida: %v", err)
	}
	endTime := startTime.Add(1 * time.Hour)

	summary := fmt.Sprintf("Turno Flowly: %s", contactName)
	desc := fmt.Sprintf("Paciente agendado vía WhatsApp.\nTeléfono: %s", contactPhone)

	event := &calendar.Event{
		Summary:     summary,
		Description: desc,
		Start: &calendar.EventDateTime{
			DateTime: startTime.Format(time.RFC3339),
		},
		End: &calendar.EventDateTime{
			DateTime: endTime.Format(time.RFC3339),
		},
	}

	_, err = c.srv.Events.Insert(c.calID, event).Do()
	return err
}
