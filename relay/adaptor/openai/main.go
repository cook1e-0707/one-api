package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/songquanpeng/one-api/common/render"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/conv"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

const (
	dataPrefix       = "data: "
	done             = "[DONE]"
	dataPrefixLength = len(dataPrefix)
)

func StreamHandler(c *gin.Context, resp *http.Response, relayMode int) (*model.ErrorWithStatusCode, string, *model.Usage) {
	responseText := ""
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	var usage *model.Usage

	common.SetEventStreamHeaders(c)

	doneRendered := false
	for scanner.Scan() {
		data := scanner.Text()
		if len(data) < dataPrefixLength { // ignore blank line or wrong format
			continue
		}
		if data[:dataPrefixLength] != dataPrefix && data[:dataPrefixLength] != done {
			continue
		}
		if strings.HasPrefix(data[dataPrefixLength:], done) {
			render.StringData(c, data)
			doneRendered = true
			continue
		}
		switch relayMode {
		case relaymode.ChatCompletions:
			var streamResponse ChatCompletionsStreamResponse
			err := json.Unmarshal([]byte(data[dataPrefixLength:]), &streamResponse)
			if err != nil {
				logger.SysError("error unmarshalling stream response: " + err.Error())
				render.StringData(c, data) // if error happened, pass the data to client
				continue                   // just ignore the error
			}
			if len(streamResponse.Choices) == 0 && streamResponse.Usage == nil {
				// but for empty choice and no usage, we should not pass it to client, this is for azure
				continue // just ignore empty choice
			}
			
			// Check if model redirection occurred and mask the model name
			if modelRedirected, _ := c.Get("model_redirected"); modelRedirected == true {
				if originalModel, exists := c.Get("original_model"); exists {
					streamResponse.Model = originalModel.(string)
					// Re-marshal the modified response
					modifiedData, err := json.Marshal(streamResponse)
					if err != nil {
						logger.SysError("error marshalling modified stream response: " + err.Error())
						render.StringData(c, data) // fallback to original data
					} else {
						render.StringData(c, dataPrefix+string(modifiedData))
					}
				} else {
					render.StringData(c, data)
				}
			} else {
				render.StringData(c, data)
			}
			for _, choice := range streamResponse.Choices {
				responseText += conv.AsString(choice.Delta.Content)
			}
			if streamResponse.Usage != nil {
				usage = streamResponse.Usage
			}
		case relaymode.Completions:
			render.StringData(c, data)
			var streamResponse CompletionsStreamResponse
			err := json.Unmarshal([]byte(data[dataPrefixLength:]), &streamResponse)
			if err != nil {
				logger.SysError("error unmarshalling stream response: " + err.Error())
				continue
			}
			for _, choice := range streamResponse.Choices {
				responseText += choice.Text
			}
		}
	}

	if err := scanner.Err(); err != nil {
		logger.SysError("error reading stream: " + err.Error())
	}

	if !doneRendered {
		render.Done(c)
	}

	err := resp.Body.Close()
	if err != nil {
		return ErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), "", nil
	}

	return nil, responseText, usage
}

func Handler(c *gin.Context, resp *http.Response, promptTokens int, modelName string) (*model.ErrorWithStatusCode, *model.Usage) {
	var textResponse SlimTextResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return ErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}
	err = json.Unmarshal(responseBody, &textResponse)
	if err != nil {
		return ErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError), nil
	}
	if textResponse.Error.Type != "" {
		return &model.ErrorWithStatusCode{
			Error:      textResponse.Error,
			StatusCode: resp.StatusCode,
		}, nil
	}
	
	// Check if model redirection occurred and mask the model name in response body
	if modelRedirected, _ := c.Get("model_redirected"); modelRedirected == true {
		if originalModel, exists := c.Get("original_model"); exists {
			// Parse response body as full TextResponse to access Model field
			var fullTextResponse TextResponse
			err = json.Unmarshal(responseBody, &fullTextResponse)
			if err == nil && fullTextResponse.Model != "" {
				// Mask the model name
				fullTextResponse.Model = originalModel.(string)
				// Re-marshal the modified response
				modifiedResponseBody, err := json.Marshal(fullTextResponse)
				if err != nil {
					logger.SysError("error marshalling modified response: " + err.Error())
					// Fallback to original response body
					resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
				} else {
					responseBody = modifiedResponseBody
					resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
				}
			} else {
				// If failed to parse as TextResponse, try string replacement as fallback
				responseStr := string(responseBody)
				if targetModel, exists := c.Get("target_model"); exists {
					modifiedResponseStr := strings.ReplaceAll(responseStr, `"model":"`+targetModel.(string)+`"`, `"model":"`+originalModel.(string)+`"`)
					responseBody = []byte(modifiedResponseStr)
				}
				resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
			}
		} else {
			resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
		}
	} else {
		// Reset response body
		resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	}

	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the HTTPClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		return ErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return ErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}

	if textResponse.Usage.TotalTokens == 0 || (textResponse.Usage.PromptTokens == 0 && textResponse.Usage.CompletionTokens == 0) {
		completionTokens := 0
		for _, choice := range textResponse.Choices {
			completionTokens += CountTokenText(choice.Message.StringContent(), modelName)
		}
		textResponse.Usage = model.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		}
	}
	return nil, &textResponse.Usage
}
