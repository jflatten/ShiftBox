FROM mcr.microsoft.com/devcontainers/base@sha256:3612b800eaae8513e7dca54f21d2e4eda48eda57426f3746a78677f342ac7ee9
RUN touch /usr/local/share/shiftbox-fixture-dockerfile \
    && install -d -o vscode -g vscode /var/lib/shiftbox-fixture \
    && install -o vscode -g vscode /dev/null /var/lib/shiftbox-fixture/.fixture
