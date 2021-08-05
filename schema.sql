create table words (
    word text not null primary key
);

create index on words (word text_pattern_ops);

create table definitions (
    id bigserial primary key,
    word text not null references words (word),
    readings text[] not null
);

create unique index on definitions (word, readings);

create table meanings (
    id bigserial primary key,
    definition_id bigint not null references definitions (id),
    meaning text not null,
    meaning_index_col tsvector
);

create index on meanings using gin (meaning_index_col);

create or replace function quote_like (text)
    returns text
    language SQL
    immutable strict
    as $func$
    select
        replace(replace(replace($1, '\', ' \\ ')
        ,' _ ', ' _ ')
        ,' % ', ' % ');

$func$;

