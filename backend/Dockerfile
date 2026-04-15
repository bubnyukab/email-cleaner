# backend/Dockerfile
FROM golang:1.26.2

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

RUN go install github.com/air-verse/air@latest

COPY . .

EXPOSE 8080

CMD ["air"]
