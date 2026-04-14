# backend/Dockerfile
FROM golang:1.26.2

WORKDIR /app

COPY go.mod ./
RUN go mod tidy

RUN go install github.com/air-verse/air@latest

COPY . .

EXPOSE 8080

CMD ["air"]
