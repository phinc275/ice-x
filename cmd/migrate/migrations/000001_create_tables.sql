-- +goose Up
CREATE TABLE x_accounts (
    "username" TEXT NOT NULL PRIMARY KEY,
    "password" TEXT,
    "email" TEXT,
    "email_password" TEXT,
    "created_at" TIMESTAMP WITH TIME ZONE NOT NULL,
    "updated_at" TIMESTAMP WITH TIME ZONE NOT NULL,
    "cookies" TEXT,
    "auto_email_confirmation_code" BOOLEAN NOT NULL
);

-- +goose Down
DROP TABLE x_accounts;