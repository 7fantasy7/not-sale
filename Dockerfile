FROM gcr.io/distroless/static-debian12

WORKDIR /app
ARG SERVICE_NAME
COPY ./build/${SERVICE_NAME} /app/service
EXPOSE 8080
USER nonroot:nonroot

CMD ["/app/service"]
