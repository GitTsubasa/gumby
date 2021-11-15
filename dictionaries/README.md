# Dictionaries

Dictionaries have the following schema, in newline-delimited JSON:

```typescript
type Entry = {
    // The word itself.
    //
    // Special characters:
    // - ?: This character cannot be represented in Unicode.
    // - _: This can be substituted for any word, in phrases.
    // - [...]: This character is composed of the radicals in between the square brackets.
    word: string;

    // All definitions of the word.
    definitions: {
        // All readings for this definition.
        //
        // The placeholder character _ may be present here with the same meaning as in the `word` field.
        readings: string[];

        // All meanings for this definition.
        meanings: string[];
    }[];
};
```
