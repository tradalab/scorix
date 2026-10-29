# The SDK, Go and the CLI are mounted from the host, so this holds only what Gradle needs.
# git because `go build` stamps VCS info.
FROM eclipse-temurin:17-jdk-jammy

RUN apt-get update \
 && apt-get install -y --no-install-recommends unzip curl ca-certificates git \
 && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL --retry 5 -o /tmp/clt.zip https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip \
 && unzip -q /tmp/clt.zip -d /opt \
 && rm /tmp/clt.zip

RUN curl -fsSL --retry 5 -o /tmp/gradle.zip https://services.gradle.org/distributions/gradle-8.10.2-bin.zip \
 && unzip -q /tmp/gradle.zip -d /opt \
 && mv /opt/gradle-8.10.2 /opt/gradle \
 && rm /tmp/gradle.zip
