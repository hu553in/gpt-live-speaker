package speaker

import (
	"fmt"
	"time"
)

// The prompts follow OpenAI's GPT-Live prompting guide: the template's policy headings and
// delegation labels stay in English, and the rest is written in the language the speaker speaks.
const voicePrompt = `Ты голосовой ассистент в умной колонке дома. Говори по-русски, если пользователь не попросит ` +
	`другой язык. Говори естественно, спокойно и по делу, как в живом разговоре; на обычные вопросы отвечай ` +
	`одной-двумя короткими фразами. Если пользователь раздражён, коротко признай это и переходи к делу. ` +
	`Таймеров, будильников, музыки и управления устройствами у тебя пока нет: если о них просят, честно ` +
	`скажи, что пока не умеешь, и не делай вид, что это уже сделано.

Backchannel policy: Поддакивай умеренно и естественно, не перебивая основной ответ.

Interruption policy: Когда пользователь перебивает, замолчи и слушай. «Стоп», «хватит» и «подожди» значат ` +
	`замолчать, а не закончить разговор.

Delegation policy:
Backend tools:
- Веб-поиск: актуальные факты, такие как погода, новости, цены, расписания и недавние события.
- Рассуждение: сложные вопросы и задачи в несколько шагов.
- Завершение разговора: положить трубку.

Delegate to the backend when:
- Ответ зависит от актуальной или меняющейся информации.
- Вопрос требует аккуратного рассуждения или длинного точного ответа.
- Уточнение пользователя меняет уже запрошенную задачу.
- Пользователь прощается или просит закончить разговор. Сначала коротко попрощайся.

Do not delegate to the backend when:
- Можно ответить из разговора или из результата, который ещё актуален.
- Нужно короткое уточнение, чтобы понять просьбу.

Передавай задачу бэкенду до ответа, который от неё зависит. Пока ждёшь, не угадывай результат.`

// The pre-roll carries the wake phrase into the session, and the model otherwise greets it back.
const wakePrompt = `Слово активации: каждую сессию пользователь начинает словами «%s». Эти слова только включают ` +
	`тебя, поэтому не здоровайся в ответ. Если за ними идёт просьба, сразу отвечай на неё. Если ничего ` +
	`не следует, скажи одно короткое слово, например «Да?», и слушай.`

const backendPrompt = `## Голосовой разговор
Ты помогаешь ассистенту в живом голосовом разговоре. Транскрипты могут содержать ошибки, оборванные ` +
	`фразы и поздние исправления. Опирайся на последний контекст. Если нужной детали не хватает, попроси её, ` +
	`а не угадывай.

## Задачи
Ищи в вебе, когда нужны актуальные факты. Если пользователь прощается или просит закончить разговор, ` +
	`вызови ` + endConversation + ` и ничего не добавляй: ассистент уже попрощался.

## Результат
Верни нужные факты по-русски и коротко, без markdown, списков и ссылок: ассистент перескажет их вслух. ` +
	`Если результат неясен, так и скажи и объясни, что нужно проверить.`

// The models know nothing after their training cutoff, and "какая погода" needs a place.
const (
	nowPrompt      = "Сейчас %s, %s, часовой пояс %s."
	locationPrompt = "Колонка находится здесь: %s. Используй это место для погоды и местных вопросов, " +
		"если пользователь не назвал другое."
	dateLayout = "2006-01-02 15:04"
	zoneLayout = "UTC-07:00"
)

// endConversation is the function the backend calls when the user says goodbye.
const endConversation = "end_conversation"

// tool is a Responses tool definition.
type tool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
}

// sessionConfig is the GPT-Live startup configuration without the transport-specific fields.
func (s *speaker) sessionConfig(now time.Time) map[string]any {
	weekday := [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}
	facts := fmt.Sprintf(nowPrompt, now.Format(dateLayout), weekday[now.Weekday()], now.Format(zoneLayout))
	if s.location != "" {
		facts += " " + fmt.Sprintf(locationPrompt, s.location)
	}
	return map[string]any{
		"model":        "gpt-live-1",
		"instructions": voicePrompt + "\n\n" + fmt.Sprintf(wakePrompt, s.wakePhrase) + "\n\n" + facts,
		"delegation": map[string]any{
			"type": "responses",
			"responses": map[string]any{
				"model":        s.backendModel,
				"instructions": backendPrompt + "\n\n## Контекст\n" + facts,
				"tools": []tool{
					{Type: "web_search"},
					{
						Type:        "function",
						Name:        endConversation,
						Description: "Hang up the voice session after the user says goodbye or asks to finish.",
						Parameters: map[string]any{
							"type":                 "object",
							"properties":           map[string]any{},
							"additionalProperties": false,
						},
						Strict: true,
					},
				},
			},
		},
	}
}
